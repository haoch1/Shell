//go:build windows

package localpty

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32                   = windows.NewLazySystemDLL("kernel32.dll")
	procCreatePseudoConsole       = modKernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole       = modKernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole        = modKernel32.NewProc("ClosePseudoConsole")
	procUpdateProcThreadAttribute = modKernel32.NewProc("UpdateProcThreadAttribute")
)

const (
	minPTYCols     = 20
	maxPTYCols     = 500
	defaultPTYCols = 100
	minPTYRows     = 5
	maxPTYRows     = 200
	defaultPTYRows = 30

	readBufSize      = 32 * 1024
	readerDrainLimit = 2 * time.Second
)

// Options configures a native Windows ConPTY session.
type Options struct {
	Command  string
	Args     []string
	Cwd      string
	Cols     int
	Rows     int
	Env      map[string]string
	OnOutput func([]byte)
	OnExit   func(exitCode int, err error)
}

// Session represents a running Windows ConPTY local terminal session protected by a Job Object
// so closing the session cleanly terminates the entire child process tree.
type Session struct {
	mu      sync.Mutex // guards job, process, closed
	hpcMu   sync.Mutex // guards hPC (ResizePseudoConsole / ClosePseudoConsole)
	writeMu sync.Mutex // guards inWrite
	outMu   sync.Mutex // guards outRead (CloseHandle vs CancelIoEx)

	hPC     windows.Handle
	job     windows.Handle
	process windows.Handle
	inWrite windows.Handle
	outRead windows.Handle
	pid     uint32
	closed  bool

	closeOnce  sync.Once
	exitOnce   sync.Once
	readerDone chan struct{}
	done       chan struct{}
	onOutput   func([]byte)
	onExit     func(exitCode int, err error)
}

func checkConPTYSupport() error {
	if err := procCreatePseudoConsole.Find(); err != nil {
		return errors.New("当前 Windows 版本不支持 ConPTY 原生终端（需要 Windows 10 1809 及以上版本）")
	}
	if err := procResizePseudoConsole.Find(); err != nil {
		return errors.New("当前 Windows 版本不支持 ConPTY 调整窗口大小")
	}
	if err := procClosePseudoConsole.Find(); err != nil {
		return errors.New("当前 Windows 版本不支持关闭 ConPTY")
	}
	if err := procUpdateProcThreadAttribute.Find(); err != nil {
		return errors.New("当前 Windows 系统缺少 UpdateProcThreadAttribute 接口")
	}
	return nil
}

func clampPTYSize(cols, rows int) (int16, int16) {
	if cols <= 0 {
		cols = defaultPTYCols
	}
	if rows <= 0 {
		rows = defaultPTYRows
	}
	if cols < minPTYCols {
		cols = minPTYCols
	} else if cols > maxPTYCols {
		cols = maxPTYCols
	}
	if rows < minPTYRows {
		rows = minPTYRows
	} else if rows > maxPTYRows {
		rows = maxPTYRows
	}
	return int16(cols), int16(rows)
}

func packCoord(cols, rows int) uintptr {
	x, y := clampPTYSize(cols, rows)
	return uintptr(uint32(uint16(x)) | (uint32(uint16(y)) << 16))
}

func createPseudoConsole(cols, rows int, inRead, outWrite windows.Handle) (windows.Handle, error) {
	var hPC windows.Handle
	r1, _, _ := procCreatePseudoConsole.Call(
		packCoord(cols, rows),
		uintptr(inRead),
		uintptr(outWrite),
		0,
		uintptr(unsafe.Pointer(&hPC)),
	)
	if r1 != 0 {
		return 0, fmt.Errorf("创建本地伪终端失败 (HRESULT 0x%08x)", uint32(r1))
	}
	return hPC, nil
}

func resizePseudoConsole(hPC windows.Handle, cols, rows int) error {
	r1, _, _ := procResizePseudoConsole.Call(
		uintptr(hPC),
		packCoord(cols, rows),
	)
	if r1 != 0 {
		return fmt.Errorf("调整本地终端尺寸失败 (HRESULT 0x%08x)", uint32(r1))
	}
	return nil
}

func closePseudoConsole(hPC windows.Handle) {
	if hPC == 0 {
		return
	}
	_, _, _ = procClosePseudoConsole.Call(uintptr(hPC))
}

func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type envPair struct {
	key string
	val string
}

func buildEnvBlock(extra map[string]string) ([]uint16, error) {
	entries := make(map[string]envPair, 64)

	setEntry := func(k, v string) {
		if k == "" {
			return
		}
		entries[strings.ToUpper(k)] = envPair{key: k, val: v}
	}

	for _, kv := range os.Environ() {
		if kv == "" {
			continue
		}
		// Windows preserves hidden per-drive cwd entries like "=C:=C:\Dir" whose key starts with '='.
		searchFrom := 0
		if kv[0] == '=' {
			searchFrom = 1
		}
		relIdx := strings.IndexByte(kv[searchFrom:], '=')
		if relIdx <= 0 {
			continue
		}
		eq := searchFrom + relIdx
		setEntry(kv[:eq], kv[eq+1:])
	}

	// Enable truecolor, xterm-256color, and modern prompt / OSC 9;9 integration.
	setEntry("TERM", "xterm-256color")
	setEntry("COLORTERM", "truecolor")
	setEntry("TERM_PROGRAM", "Shell")
	setEntry("WT_SESSION", newUUIDv4())
	if _, hasLang := entries["LANG"]; !hasLang {
		setEntry("LANG", "C.UTF-8")
	}

	for k, v := range extra {
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsRune(k, '=') || strings.ContainsRune(k, 0) {
			continue
		}
		if strings.ContainsRune(v, 0) {
			continue
		}
		setEntry(k, v)
	}

	keys := make([]string, 0, len(entries))
	for upper := range entries {
		keys = append(keys, upper)
	}
	sort.Strings(keys)

	block := make([]uint16, 0, len(keys)*32+2)
	for _, upper := range keys {
		p := entries[upper]
		line := p.key + "=" + p.val
		if strings.ContainsRune(line, 0) {
			continue
		}
		block = append(block, utf16.Encode([]rune(line))...)
		block = append(block, 0)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	block = append(block, 0)
	return block, nil
}

func resolveWorkingDir(cwd string) string {
	if d := strings.TrimSpace(cwd); d != "" {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			return d
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if info, err := os.Stat(home); err == nil && info.IsDir() {
			return home
		}
	}
	return ""
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// Start launches a local shell attached to a native Windows ConPTY and returns a live Session.
func Start(opts Options) (*Session, error) {
	if err := checkConPTYSupport(); err != nil {
		return nil, err
	}
	cmd := strings.TrimSpace(opts.Command)
	if cmd == "" {
		return nil, errors.New("未指定本地终端可执行文件")
	}

	resolvedCmd := cmd
	if !filepath.IsAbs(resolvedCmd) && !strings.ContainsAny(resolvedCmd, `\/`) {
		if found, err := exec.LookPath(resolvedCmd); err == nil {
			resolvedCmd = found
		}
	}

	var ptyInRead, ptyInWrite, ptyOutRead, ptyOutWrite windows.Handle
	if err := windows.CreatePipe(&ptyInRead, &ptyInWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("创建终端输入管道失败：%w", err)
	}
	closeInRead := true
	closeInWrite := true
	defer func() {
		if closeInRead && ptyInRead != 0 {
			_ = windows.CloseHandle(ptyInRead)
		}
		if closeInWrite && ptyInWrite != 0 {
			_ = windows.CloseHandle(ptyInWrite)
		}
	}()

	if err := windows.CreatePipe(&ptyOutRead, &ptyOutWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("创建终端输出管道失败：%w", err)
	}
	closeOutRead := true
	closeOutWrite := true
	defer func() {
		if closeOutRead && ptyOutRead != 0 {
			_ = windows.CloseHandle(ptyOutRead)
		}
		if closeOutWrite && ptyOutWrite != 0 {
			_ = windows.CloseHandle(ptyOutWrite)
		}
	}()

	hPC, err := createPseudoConsole(opts.Cols, opts.Rows, ptyInRead, ptyOutWrite)
	if err != nil {
		return nil, err
	}
	closeHPC := true
	defer func() {
		if closeHPC && hPC != 0 {
			closePseudoConsole(hPC)
		}
	}()

	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, fmt.Errorf("初始化进程属性列表失败：%w", err)
	}
	defer attrList.Delete()

	// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE expects the HPCON handle value itself as lpValue.
	// Calling UpdateProcThreadAttribute directly avoids converting a Windows handle to unsafe.Pointer.
	r1, _, lastErr := procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(attrList.List())),
		0,
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		uintptr(hPC),
		unsafe.Sizeof(hPC),
		0,
		0,
	)
	if r1 == 0 {
		return nil, fmt.Errorf("绑定伪终端进程属性失败：%w", lastErr)
	}

	envBlock, err := buildEnvBlock(opts.Env)
	if err != nil {
		return nil, fmt.Errorf("构建终端环境变量失败：%w", err)
	}

	argv := make([]string, 0, 1+len(opts.Args))
	argv = append(argv, resolvedCmd)
	argv = append(argv, opts.Args...)
	cmdLinePtr, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return nil, fmt.Errorf("构建启动命令行失败：%w", err)
	}

	var appNamePtr *uint16
	if fileExists(resolvedCmd) {
		appNamePtr, err = windows.UTF16PtrFromString(resolvedCmd)
		if err != nil {
			return nil, fmt.Errorf("解析终端路径失败：%w", err)
		}
	}

	var cwdPtr *uint16
	if cwd := resolveWorkingDir(opts.Cwd); cwd != "" {
		cwdPtr, err = windows.UTF16PtrFromString(cwd)
		if err != nil {
			return nil, fmt.Errorf("解析工作目录失败：%w", err)
		}
	}

	job, jobErr := createKillOnCloseJob()
	closeJob := jobErr == nil && job != 0
	defer func() {
		if closeJob && job != 0 {
			_ = windows.CloseHandle(job)
		}
	}()

	var si windows.StartupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si))
	// Explicitly clear inherited redirected stdio handles (e.g. under `go test` or redirected
	// launchers) so the child attaches strictly to ConPTY's CONIN$/CONOUT$ instead of parent pipes.
	si.Flags = windows.STARTF_USESTDHANDLES
	si.ProcThreadAttributeList = attrList.List()

	var pi windows.ProcessInformation
	creationFlags := uint32(
		windows.EXTENDED_STARTUPINFO_PRESENT |
			windows.CREATE_UNICODE_ENVIRONMENT |
			windows.CREATE_SUSPENDED,
	)
	err = windows.CreateProcess(
		appNamePtr,
		cmdLinePtr,
		nil,
		nil,
		false,
		creationFlags,
		&envBlock[0],
		cwdPtr,
		&si.StartupInfo,
		&pi,
	)
	runtime.KeepAlive(envBlock)
	if err != nil {
		return nil, fmt.Errorf("启动本地终端进程失败：%w", err)
	}

	// Immediately close pipe ends now owned by ConPTY; otherwise ptyOutRead will never reach EOF.
	_ = windows.CloseHandle(ptyInRead)
	ptyInRead = 0
	closeInRead = false
	_ = windows.CloseHandle(ptyOutWrite)
	ptyOutWrite = 0
	closeOutWrite = false

	if job != 0 {
		if assignErr := windows.AssignProcessToJobObject(job, pi.Process); assignErr != nil {
			// Fall back gracefully if a restricted parent job forbids nested job assignment.
			_ = windows.CloseHandle(job)
			job = 0
			closeJob = false
		}
	}

	if _, resumeErr := windows.ResumeThread(pi.Thread); resumeErr != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		_ = windows.CloseHandle(pi.Thread)
		_ = windows.CloseHandle(pi.Process)
		return nil, fmt.Errorf("恢复本地终端主线程失败：%w", resumeErr)
	}
	_ = windows.CloseHandle(pi.Thread)

	// Ownership of remaining handles transfers to Session.
	closeInWrite = false
	closeOutRead = false
	closeHPC = false
	closeJob = false

	s := &Session{
		hPC:        hPC,
		job:        job,
		process:    pi.Process,
		inWrite:    ptyInWrite,
		outRead:    ptyOutRead,
		pid:        pi.ProcessId,
		readerDone: make(chan struct{}),
		done:       make(chan struct{}),
		onOutput:   opts.OnOutput,
		onExit:     opts.OnExit,
	}

	go s.readLoop()
	go s.waitLoop()

	return s, nil
}

// PID returns the OS process ID of the launched shell process.
func (s *Session) PID() int {
	if s == nil {
		return 0
	}
	return int(s.pid)
}

// Done returns a channel that is closed after the session has exited and all handles are cleaned up.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

func (s *Session) readLoop() {
	s.outMu.Lock()
	outRead := s.outRead
	s.outMu.Unlock()

	defer func() {
		s.outMu.Lock()
		h := s.outRead
		s.outRead = 0
		s.outMu.Unlock()
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		close(s.readerDone)
	}()

	if outRead == 0 {
		return
	}

	buf := make([]byte, readBufSize)
	for {
		var n uint32
		err := windows.ReadFile(outRead, buf, &n, nil)
		if n > 0 && s.onOutput != nil {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.onOutput(chunk)
		}
		if err != nil || n == 0 {
			return
		}
	}
}

func (s *Session) waitLoop() {
	s.mu.Lock()
	proc := s.process
	s.mu.Unlock()

	var exitCode uint32
	var waitErr error
	if proc != 0 {
		event, err := windows.WaitForSingleObject(proc, windows.INFINITE)
		if err != nil {
			waitErr = err
		} else if event != windows.WAIT_OBJECT_0 {
			waitErr = fmt.Errorf("等待本地终端退出异常 (状态 0x%x)", event)
		}
		if err := windows.GetExitCodeProcess(proc, &exitCode); err != nil && waitErr == nil {
			waitErr = err
		}
	}

	// Close the pseudo console first so ConPTY flushes all buffered output and signals EOF on outRead.
	s.closePseudoConsole()

	// Wait for readLoop to finish draining output without truncating final bytes.
	timer := time.NewTimer(readerDrainLimit)
	select {
	case <-s.readerDone:
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	case <-timer.C:
		s.outMu.Lock()
		if s.outRead != 0 {
			_ = windows.CancelIoEx(s.outRead, nil)
		}
		s.outMu.Unlock()
		select {
		case <-s.readerDone:
		case <-time.After(500 * time.Millisecond):
		}
	}

	// Clean up remaining handles cleanly.
	s.closeJob()
	s.closeInWrite()

	s.mu.Lock()
	p := s.process
	s.process = 0
	s.closed = true
	s.mu.Unlock()
	if p != 0 {
		_ = windows.CloseHandle(p)
	}

	close(s.done)
	s.fireExit(int(exitCode), waitErr)
}

func (s *Session) closePseudoConsole() {
	s.hpcMu.Lock()
	h := s.hPC
	s.hPC = 0
	s.hpcMu.Unlock()
	if h != 0 {
		closePseudoConsole(h)
	}
}

func (s *Session) closeJob() {
	s.mu.Lock()
	job := s.job
	s.job = 0
	s.mu.Unlock()
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
}

func (s *Session) closeInWrite() {
	s.writeMu.Lock()
	h := s.inWrite
	s.inWrite = 0
	s.writeMu.Unlock()
	if h != 0 {
		_ = windows.CloseHandle(h)
	}
}

func (s *Session) fireExit(exitCode int, err error) {
	s.exitOnce.Do(func() {
		if s.onExit != nil {
			s.onExit(exitCode, err)
		}
	})
}

// Write sends raw UTF-8/VT input bytes to the pseudo console input pipe.
func (s *Session) Write(p []byte) error {
	if s == nil {
		return errors.New("本地终端会话未连接")
	}
	if len(p) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.inWrite == 0 {
		return errors.New("本地终端会话已关闭")
	}
	remaining := p
	for len(remaining) > 0 {
		var written uint32
		err := windows.WriteFile(s.inWrite, remaining, &written, nil)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	return nil
}

// Resize updates the ConPTY viewport dimensions (clamped to 20..500 cols and 5..200 rows).
func (s *Session) Resize(cols, rows int) error {
	if s == nil {
		return errors.New("本地终端会话未连接")
	}
	s.hpcMu.Lock()
	defer s.hpcMu.Unlock()
	if s.hPC == 0 {
		return nil
	}
	return resizePseudoConsole(s.hPC, cols, rows)
}

// Close idempotently terminates the entire shell process tree via the Job Object and
// releases all ConPTY and pipe resources without leaking handles or goroutines.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		job := s.job
		s.job = 0
		proc := s.process
		s.mu.Unlock()

		// 1. Kill the entire process tree first via JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
		if job != 0 {
			_ = windows.CloseHandle(job)
		} else if proc != 0 {
			_ = windows.TerminateProcess(proc, 1)
		}

		// 2. Close ConPTY so conhost closes both ends of the pipes (unblocking ReadFile & WriteFile).
		s.closePseudoConsole()

		// 3. Close the input write pipe now that any blocked WriteFile is unblocked.
		s.closeInWrite()
	})
	return nil
}
