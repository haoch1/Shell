package remote

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestConnectPreservesFollowingTunnelBytes(t *testing.T) {
	const banner = "SSH-2.0-unit-server\r\n"
	for _, coalesced := range []bool{true, false} {
		name := "separate-writes"
		if coalesced {
			name = "same-write"
		}
		t.Run(name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			client.SetDeadline(time.Now().Add(2 * time.Second))
			go func() {
				header := "HTTP/1.1 200 Connection Established\r\n\r\n"
				if coalesced {
					io.WriteString(server, header+banner)
				} else {
					io.WriteString(server, header)
					io.WriteString(server, banner)
				}
			}()
			conn, err := readConnectResponse(client)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(banner))
			if _, err := io.ReadFull(conn, got); err != nil || string(got) != banner {
				t.Fatalf("tunneled banner = %q, err = %v", got, err)
			}
		})
	}
}
