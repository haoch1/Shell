//go:build !windows

package agentbridge

import (
	"context"
	"net"
	"time"
)

func listenDesktopPipe() (net.Listener, string, error) { return nil, "", nil }

func (e Endpoint) dial(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, address)
}
