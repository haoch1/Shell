package agentbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const pipePrefix = "\\\\.\\pipe\\Shell-MCP-"

func listenDesktopPipe() (net.Listener, string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, "", err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, "", err
	}
	id := hex.EncodeToString(nonce[:])
	// Only the desktop account can open the pipe. go-winio also sets FILE_PIPE_REJECT_REMOTE_CLIENTS.
	sid := user.User.Sid.String()
	ln, err := winio.ListenPipe(pipePrefix+id, &winio.PipeConfig{
		SecurityDescriptor: "O:" + sid + "D:P(D;;GA;;;NU)(A;;GA;;;" + sid + ")",
		InputBufferSize:    32 << 10,
		OutputBufferSize:   32 << 10,
	})
	if err != nil {
		return nil, "", err
	}
	return ln, id, nil
}

func (e Endpoint) dial(ctx context.Context, network, address string) (net.Conn, error) {
	tcp := net.Dialer{Timeout: 2 * time.Second}
	if e.PipeID == "" {
		return tcp.DialContext(ctx, network, address)
	}
	if len(e.PipeID) != 32 || e.PID <= 0 {
		return nil, errors.New("Shell MCP 管道信息无效")
	}
	if _, err := hex.DecodeString(e.PipeID); err != nil {
		return nil, errors.New("Shell MCP 管道标识无效")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	// READ_CONTROL allows checking the endpoint owner before sending the bearer token.
	conn, err := winio.DialPipeAccess(dialCtx, pipePrefix+e.PipeID, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL)
	cancel()
	if err != nil {
		// A missing or busy pipe can fall back before net/http has written any request.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, context.DeadlineExceeded) {
			return tcp.DialContext(ctx, network, address)
		}
		return nil, err
	}
	file, ok := conn.(interface{ Fd() uintptr })
	if ok {
		handle := windows.Handle(file.Fd())
		var pid uint32
		if windows.GetNamedPipeServerProcessId(handle, &pid) == nil && int(pid) == e.PID && hasDesktopOwner(handle) {
			return conn, nil
		}
	}
	conn.Close()
	return nil, errors.New("Shell MCP 管道身份验证失败")
}

func hasDesktopOwner(handle windows.Handle) bool {
	sd, err := windows.GetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && owner.Equals(user.User.Sid)
}
