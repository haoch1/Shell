package remote

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// SFTP v3 packet types. For these metadata operations pkg/sftp writes one complete frame.
const (
	fxpClose    = 4
	fxpOpendir  = 11
	fxpReaddir  = 12
	fxpRealpath = 16
	fxpStat     = 17
)

type directoryProbe struct {
	net.Conn
	packets map[byte]int
	delay   time.Duration
}

func (p *directoryProbe) Write(b []byte) (int, error) {
	if p.packets != nil && len(b) >= 5 && binary.BigEndian.Uint32(b) == uint32(len(b)-4) {
		p.packets[b[4]]++
		if p.delay != 0 {
			time.Sleep(p.delay)
		}
	}
	return p.Conn.Write(b)
}

func linkDirectory(tb testing.TB, links int) (*Session, *directoryProbe) {
	tb.Helper()
	serverEnd, clientEnd := net.Pipe()
	server := sftp.NewRequestServer(serverEnd, sftp.InMemHandler())
	go server.Serve()
	tb.Cleanup(func() { clientEnd.Close(); server.Close() })
	probe := &directoryProbe{Conn: clientEnd}
	c, err := sftp.NewClientPipe(probe, probe)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { c.Close() })
	for _, dir := range []string{"/listing", "/target"} {
		if err := c.Mkdir(dir); err != nil {
			tb.Fatal(err)
		}
	}
	for i := range links {
		target := "/target"
		if i == links-1 {
			target = "/missing"
		}
		if err := c.Symlink(target, fmt.Sprintf("/listing/link-%02d", i)); err != nil {
			tb.Fatal(err)
		}
	}
	probe.packets = make(map[byte]int)
	return &Session{files: c}, probe
}

func TestDirectoryBrowseRoundTrips(t *testing.T) {
	const links = 24
	s, probe := linkDirectory(t, links)
	listing, err := s.BrowseDirectory(t.Context(), "listing")
	if err != nil || listing.Path != "/listing" || len(listing.Entries) != links {
		t.Fatalf("unexpected listing: %+v, %v", listing, err)
	}
	for _, e := range listing.Entries {
		if !e.IsSymlink || e.IsDir || e.Path != "/listing/"+e.Name {
			t.Fatalf("unexpected link: %+v", e)
		}
	}
	p := probe.packets
	if p[fxpRealpath] != 1 || p[fxpOpendir] != 1 || p[fxpReaddir] != 2 || p[fxpClose] != 1 || p[fxpStat] != 0 {
		t.Fatalf("directory packet counts: %v", p)
	}
	link, err := s.ResolveFileLink("/listing/link-00")
	if err != nil || !link.IsDir || link.Name != "link-00" || link.Path != "/listing/link-00" || p[fxpStat] != 1 {
		t.Fatalf("selected link: %+v, %v, requests %v", link, err, p)
	}
	if _, err := s.ResolveFileLink("/listing/link-23"); err == nil {
		t.Fatal("broken link unexpectedly resolved")
	}
	clear(p)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.BrowseDirectory(ctx, "listing"); !errors.Is(err, context.Canceled) || len(p) != 0 {
		t.Fatalf("canceled browse: %v, requests %v", err, p)
	}
	canonical, err := s.RealPath("listing")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := s.List(canonical)
	if err != nil || len(legacy) != links || !legacy[0].IsDir || p[fxpRealpath] != 2 || p[fxpStat] != links {
		t.Fatalf("legacy listing: %v, requests %v", err, p)
	}
}

func BenchmarkSFTPDirectoryBrowse(b *testing.B) {
	for _, mode := range []string{"legacy", "browse"} {
		b.Run(mode, func(b *testing.B) {
			s, probe := linkDirectory(b, 24)
			probe.delay = 5 * time.Millisecond
			b.ResetTimer()
			for range b.N {
				if mode == "legacy" {
					canonical, err := s.RealPath("listing")
					if err != nil {
						b.Fatal(err)
					}
					if _, err = s.List(canonical); err != nil {
						b.Fatal(err)
					}
				} else if _, err := s.BrowseDirectory(b.Context(), "listing"); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			total := 0
			for _, n := range probe.packets {
				total += n
			}
			b.ReportMetric(float64(total)/float64(b.N), "requests/op")
			b.ReportMetric(float64(probe.packets[fxpStat])/float64(b.N), "stat/op")
		})
	}
}
