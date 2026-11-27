package remote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshExecWorker keeps a single lightweight non-interactive `/bin/sh -s` SSH channel open
// so repeated MCP / telemetry commands execute in 1 RTT instead of 3-4 RTTs per channel open.
type sshExecWorker struct {
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bufio.Reader
	closed atomic.Bool
}

// limitedBuffer caps captured bytes while continuing to accept writes so SSH channels never stall on flow control.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.limit > 0 {
		remain := l.limit - int64(l.buf.Len())
		if remain > 0 {
			if int64(len(p)) > remain {
				l.buf.Write(p[:remain])
				l.truncated = true
			} else {
				l.buf.Write(p)
			}
		} else if len(p) > 0 {
			l.truncated = true
		}
	}
	return len(p), nil
}

func startExecWorker(client *ssh.Client) (*sshExecWorker, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stdoutPipe, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stderrPipe, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	if err := sess.Start("sh -s"); err != nil {
		sess.Close()
		return nil, err
	}
	return &sshExecWorker{
		sess:   sess,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdoutPipe, 32<<10),
		stderr: bufio.NewReaderSize(stderrPipe, 16<<10),
	}, nil
}

func (w *sshExecWorker) isClosed() bool {
	return w == nil || w.closed.Load()
}

func (w *sshExecWorker) close() {
	if w == nil || !w.closed.CompareAndSwap(false, true) {
		return
	}
	_ = w.stdin.Close()
	_ = w.sess.Close()
}

// readUntilMarker streams lines in fixed chunks so huge single-line outputs never allocate unbounded memory.
func readUntilMarker(r *bufio.Reader, marker string, dst *limitedBuffer) (string, error) {
	markerBytes := []byte(marker)
	atLineStart := true
	for {
		frag, err := r.ReadSlice('\n')
		if atLineStart {
			if idx := bytes.Index(frag, markerBytes); idx >= 0 {
				if idx > 0 {
					_, _ = dst.Write(frag[:idx])
				}
				tail := strings.TrimSpace(string(frag[idx+len(markerBytes):]))
				for errors.Is(err, bufio.ErrBufferFull) {
					var more []byte
					more, err = r.ReadSlice('\n')
					tail += strings.TrimSpace(string(more))
				}
				return tail, nil
			}
		}
		if len(frag) > 0 {
			_, _ = dst.Write(frag)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			atLineStart = false
			continue
		}
		if err != nil {
			return "", err
		}
		atLineStart = true
	}
}

func (w *sshExecWorker) run(ctx context.Context, command string, maxBytes int64) (ExecResult, error) {
	if w.isClosed() {
		return ExecResult{}, errors.New("worker closed")
	}
	t0 := time.Now()
	var nonceBytes [12]byte
	_, _ = rand.Read(nonceBytes[:])
	token := "__SH_MCP_END_" + hex.EncodeToString(nonceBytes[:]) + "__"
	stdoutMarker := token + ":"
	stderrMarker := token

	// Frame the command safely; if command contains newlines, terminate cleanly before sentinel.
	script := fmt.Sprintf("{\n%s\n}\n__sh_ec=$?\nprintf '\\n%s%%d\\n' \"$__sh_ec\"\nprintf '\\n%s\\n' >&2\n", command, stdoutMarker, stderrMarker)

	stop := context.AfterFunc(ctx, func() {
		w.close()
	})
	defer stop()

	if _, err := io.WriteString(w.stdin, script); err != nil {
		w.close()
		return ExecResult{}, err
	}

	if maxBytes <= 0 {
		maxBytes = 256 << 10
	}
	outBuf := limitedBuffer{limit: maxBytes}
	errBuf := limitedBuffer{limit: max(1, maxBytes/2)}
	var outErr, errErr error
	exitCode := 0

	var wg sync.WaitGroup
	wg.Add(2)

	// Read stdout until stdoutMarker line
	go func() {
		defer wg.Done()
		codeStr, err := readUntilMarker(w.stdout, stdoutMarker, &outBuf)
		if err != nil {
			outErr = err
			return
		}
		if n, pErr := strconv.Atoi(codeStr); pErr == nil {
			exitCode = n
		}
	}()

	// Read stderr until stderrMarker line
	go func() {
		defer wg.Done()
		_, err := readUntilMarker(w.stderr, stderrMarker, &errBuf)
		if err != nil {
			errErr = err
		}
	}()

	wg.Wait()

	stdoutStr := outBuf.buf.String()
	stderrStr := errBuf.buf.String()
	if ctx.Err() != nil {
		w.close()
		return ExecResult{
			Stdout:     strings.TrimSuffix(stdoutStr, "\n"),
			Stderr:     strings.TrimSuffix(stderrStr, "\n"),
			ExitCode:   -1,
			DurationMs: time.Since(t0).Milliseconds(),
		}, ctx.Err()
	}
	if outErr != nil || errErr != nil {
		w.close()
		if outErr != nil {
			return ExecResult{ExitCode: -1, DurationMs: time.Since(t0).Milliseconds()}, outErr
		}
		return ExecResult{ExitCode: -1, DurationMs: time.Since(t0).Milliseconds()}, errErr
	}

	// Trim the single sentinel leading newline we injected before the marker (unless truncated beforehand).
	if !outBuf.truncated {
		stdoutStr = strings.TrimSuffix(stdoutStr, "\n")
	}
	if !errBuf.truncated {
		stderrStr = strings.TrimSuffix(stderrStr, "\n")
	}
	return ExecResult{
		Stdout:     stdoutStr,
		Stderr:     stderrStr,
		ExitCode:   exitCode,
		DurationMs: time.Since(t0).Milliseconds(),
	}, nil
}

// ConnectHeadless establishes a lightweight background SSH transport (with ProxyJump & proxy support)
// without allocating an interactive PTY, optimized for silent MCP execution even when minimized to tray.
func ConnectHeadless(ctx context.Context, cfg Config, cb Callbacks) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	setup, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	t0 := time.Now()
	client, ownedJumps, releaseJumps, stops, err := dialSSHClient(ctx, setup, cfg, cb, nil, 0)
	if err != nil {
		return nil, err
	}
	initialRTT := time.Since(t0).Milliseconds()

	keepCtx, stopKeep := context.WithCancel(ctx)
	s := &Session{
		client:       client,
		jumpClients:  ownedJumps,
		releaseJumps: releaseJumps,
		stops:        stops,
		stopKeep:     stopKeep,
		tunnels:      make(map[string]*activeTunnel),
	}
	s.latencyMs.Store(initialRTT)
	if cb.Latency != nil {
		cb.Latency(initialRTT)
	}
	// Pre-warm the persistent `sh -s` worker concurrently while returning to the caller.
	s.startWarmExecWorkerAsync()

	keepSec := cfg.KeepAliveSec
	if keepSec <= 0 {
		keepSec = 30
	}
	go s.keepAliveLoop(keepCtx, time.Duration(keepSec)*time.Second, cb.Latency)

	go func() {
		err := client.Wait()
		explicit := s.closed.Load()
		s.Close()
		if cb.End != nil {
			if explicit {
				err = nil
			}
			cb.End(err)
		}
	}()
	return s, nil
}

// execOneShot runs a command over a dedicated SSH session channel (used for concurrent calls or fallback).
func (s *Session) execOneShot(ctx context.Context, command string, maxBytes int64) (ExecResult, error) {
	if s.closed.Load() {
		return ExecResult{}, net.ErrClosed
	}
	t0 := time.Now()
	sess, err := s.client.NewSession()
	if err != nil {
		return ExecResult{}, err
	}
	defer sess.Close()

	stdout, err := sess.StdoutPipe()
	if err != nil {
		return ExecResult{}, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return ExecResult{}, err
	}
	if err := sess.Start(command); err != nil {
		return ExecResult{}, err
	}
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	defer stop()

	if maxBytes <= 0 {
		maxBytes = 256 << 10
	}
	outBuf := limitedBuffer{limit: maxBytes}
	errBuf := limitedBuffer{limit: max(1, maxBytes/2)}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&outBuf, stdout)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&errBuf, stderr)
	}()
	wg.Wait()

	waitErr := sess.Wait()
	res := ExecResult{
		Stdout:     outBuf.buf.String(),
		Stderr:     errBuf.buf.String(),
		ExitCode:   0,
		DurationMs: time.Since(t0).Milliseconds(),
	}
	if waitErr != nil {
		var exitErr *ssh.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitStatus()
		} else {
			res.ExitCode = -1
		}
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, waitErr
}
