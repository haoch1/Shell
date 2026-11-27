package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haoch1/Shell/frontend"
	"github.com/haoch1/Shell/internal/local"
	"github.com/haoch1/Shell/internal/remote"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestAppInfoUsesManifestVersion(t *testing.T) {
	if got, want := newApp().AppInfo().Version, frontend.Version(); got != want {
		t.Fatalf("desktop version %q differs from embedded manifest %q", got, want)
	}
}

func TestExplainTerminalStopsOnInvalidRedactionSettings(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	dir := filepath.Join(root, "Shell")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	app := newApp()
	app.ctx = t.Context()
	text, err := app.ExplainTerminal("sample terminal output", "explain")
	if text != "" || err == nil || !strings.Contains(err.Error(), "脱敏设置") {
		t.Fatalf("expected redaction settings error, got %q, %v", text, err)
	}
}

func TestPreferencesKeepConcurrentTrayAndWindowChanges(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	app := newApp()
	finished := make(chan error, 2)
	go func() {
		_, err := local.UpdateSettings(func(s *local.AppSettings) error {
			s.WindowWidth = 1450
			s.WindowHeight = 950
			return nil
		})
		finished <- err
	}()
	go func() {
		_, err := app.UpdatePreferences(map[string]any{"fontSize": 17, "windowWidth": 880})
		finished <- err
	}()
	app.ToggleCloseToTray()
	for range 2 {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.UpdatePreferences(map[string]any{"theme": "dark", "fontSize": "invalid"}); err == nil {
		t.Fatal("accepted invalid preference value")
	}
	s, err := local.LoadSettings()
	if err != nil || s.WindowWidth != 1450 || s.WindowHeight != 950 || s.FontSize != 17 || s.CloseToTray || s.Theme != "light" {
		t.Fatalf("lost or partially applied a preference update: %+v, %v", s, err)
	}

	// Verify hideHostIp privacy preference persists on/off and rejects invalid types.
	if s.HideHostIP {
		t.Fatal("expected default hideHostIp to be false")
	}
	if updated, err := app.UpdatePreferences(map[string]any{"hideHostIp": true}); err != nil || !updated.HideHostIP {
		t.Fatalf("failed to enable hideHostIp: %+v, %v", updated, err)
	}
	if reloaded, err := local.LoadSettings(); err != nil || !reloaded.HideHostIP || reloaded.FontSize != 17 {
		t.Fatalf("hideHostIp did not persist cleanly: %+v, %v", reloaded, err)
	}
	if updated, err := app.UpdatePreferences(map[string]any{"hideHostIp": false}); err != nil || updated.HideHostIP {
		t.Fatalf("failed to disable hideHostIp: %+v, %v", updated, err)
	}
	if _, err := app.UpdatePreferences(map[string]any{"hideHostIp": "yes"}); err == nil {
		t.Fatal("accepted non-boolean hideHostIp value")
	}
}

func TestSaveProfileWithSecretRetainsPasswordOnEdit(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	app := newApp()

	// 1. Save a new host with "仅保存" (SaveProfileWithSecret) and password.
	created, err := app.SaveProfileWithSecret(local.Profile{
		Name: "SG - AWS",
		Host: "56.10.113.238",
		Port: 58222,
		User: "root",
		Auth: "password",
	}, "super-secret-pass", true, false)
	if err != nil {
		t.Fatalf("SaveProfileWithSecret create failed: %v", err)
	}
	if !created.Remembered {
		t.Fatal("expected created profile to have remembered=true")
	}
	gotSec, err := app.GetProfileSecret(created.ID)
	if err != nil || gotSec != "super-secret-pass" {
		t.Fatalf("GetProfileSecret returned %q, err=%v", gotSec, err)
	}

	// 2. Edit host from IPv4 to IPv6 without re-entering password (keepExisting=true).
	updated, err := app.SaveProfileWithSecret(local.Profile{
		ID:   created.ID,
		Name: "SG - AWS",
		Host: "2406:da18:b15:8000:4839:f12a:9c3b:4e21",
		Port: 58222,
		User: "root",
		Auth: "password",
	}, "", true, true)
	if err != nil {
		t.Fatalf("SaveProfileWithSecret IPv6 edit failed: %v", err)
	}
	if !updated.Remembered {
		t.Fatal("expected password to stay remembered after editing IP to IPv6")
	}
	gotSec, err = app.GetProfileSecret(updated.ID)
	if err != nil || gotSec != "super-secret-pass" {
		t.Fatalf("password lost after IP edit: %q, err=%v", gotSec, err)
	}
	saved, ok, err := local.GetProfile(updated.ID)
	if err != nil || !ok {
		t.Fatal("missing saved profile", err)
	}
	cleared, err := app.SaveProfileWithSecret(saved, "", true, false)
	if err != nil || cleared.Remembered {
		t.Fatal("explicitly cleared password was retained", err)
	}
}

func TestProfileCacheInvalidatesOnMutations(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	app := newApp()

	// Warm empty cache
	initial, err := app.getCachedProfiles()
	if err != nil || len(initial) != 0 {
		t.Fatalf("expected empty cached profiles, got %+v err=%v", initial, err)
	}

	// SaveProfile must invalidate cache immediately
	p1, err := app.SaveProfile(local.Profile{Name: "alpha", Host: "10.0.0.1", User: "root", Auth: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	found, err := app.findSavedProfile("alpha")
	if err != nil || found.ID != p1.ID {
		t.Fatalf("cache missed newly saved profile: %+v err=%v", found, err)
	}

	// Rename (same byte length!) via SaveProfile must still invalidate immediately
	p1Updated, ok, err := app.getCachedProfileByID(p1.ID)
	if err != nil || !ok {
		t.Fatalf("getCachedProfileByID failed: ok=%v err=%v", ok, err)
	}
	p1Updated.Name = "bravo"
	if _, err := app.SaveProfile(p1Updated); err != nil {
		t.Fatal(err)
	}
	if _, err := app.findSavedProfile("alpha"); err == nil {
		t.Fatal("stale profile name remained in cache after SaveProfile")
	}
	if got, err := app.findSavedProfile("bravo"); err != nil || got.ID != p1.ID {
		t.Fatalf("updated profile name not visible after SaveProfile: %+v err=%v", got, err)
	}

	// SetProfilesGroup must invalidate cache immediately
	if err := app.SetProfilesGroup([]string{p1.ID}, "Production"); err != nil {
		t.Fatalf("SetProfilesGroup failed: %v", err)
	}
	if got, ok, err := app.getCachedProfileByID(p1.ID); err != nil || !ok || got.Group != "Production" {
		t.Fatalf("cache did not reflect SetProfilesGroup: %+v ok=%v err=%v", got, ok, err)
	}

	// DuplicateProfile + DeleteProfile + DeleteProfiles must invalidate cache cleanly
	dup, err := app.DuplicateProfile(p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := app.getCachedProfiles(); err != nil || len(list) != 2 {
		t.Fatalf("expected 2 profiles after duplicate, got %d err=%v", len(list), err)
	}
	if err := app.DeleteProfile(dup.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := app.getCachedProfileByID(dup.ID); err != nil || ok {
		t.Fatalf("deleted profile remained in cache: ok=%v err=%v", ok, err)
	}
	if err := app.DeleteProfiles([]string{p1.ID}); err != nil {
		t.Fatal(err)
	}
	if list, err := app.getCachedProfiles(); err != nil || len(list) != 0 {
		t.Fatalf("expected 0 profiles after DeleteProfiles, got %d err=%v", len(list), err)
	}
}

type appTestSSHServer struct {
	addr       *net.TCPAddr
	pubKey     ssh.PublicKey
	handshakes atomic.Int32
}

func startAppTestSSH(t *testing.T) *appTestSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	sc := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if meta.User() == "test" && string(pass) == "pw" {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	sc.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &appTestSSHServer{
		addr:   ln.Addr().(*net.TCPAddr),
		pubKey: hostSigner.PublicKey(),
	}
	var wg sync.WaitGroup
	var conns sync.Map
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Store(sock, true)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conns.Delete(sock)
				defer sock.Close()
				conn, chans, reqs, err := ssh.NewServerConn(sock, sc)
				if err != nil {
					return
				}
				srv.handshakes.Add(1)
				defer conn.Close()
				go func() {
					for r := range reqs {
						r.Reply(r.Type == "keepalive@openssh.com", nil)
					}
				}()
				for c := range chans {
					switch c.ChannelType() {
					case "direct-tcpip":
						var p struct {
							DestHost   string
							DestPort   uint32
							OriginHost string
							OriginPort uint32
						}
						if ssh.Unmarshal(c.ExtraData(), &p) != nil {
							c.Reject(ssh.Prohibited, "bad")
							continue
						}
						dst, err := net.DialTimeout("tcp", net.JoinHostPort(p.DestHost, strconv.Itoa(int(p.DestPort))), 5*time.Second)
						if err != nil {
							c.Reject(ssh.ConnectionFailed, err.Error())
							continue
						}
						ch, rqs, err := c.Accept()
						if err != nil {
							dst.Close()
							continue
						}
						go ssh.DiscardRequests(rqs)
						go func() {
							defer ch.Close()
							defer dst.Close()
							done := make(chan struct{}, 2)
							go func() { io.Copy(dst, ch); done <- struct{}{} }()
							go func() { io.Copy(ch, dst); done <- struct{}{} }()
							<-done
						}()
					case "session":
						ch, rqs, err := c.Accept()
						if err == nil {
							go func() {
								defer ch.Close()
								for r := range rqs {
									r.Reply(false, nil)
								}
							}()
						}
					default:
						c.Reject(ssh.UnknownChannelType, "unsupported")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-stopped
		conns.Range(func(k, _ any) bool { k.(net.Conn).Close(); return true })
		wg.Wait()
	})
	return srv
}

func pinTestKnownHosts(t *testing.T, root string, srvs ...*appTestSSHServer) {
	t.Helper()
	dir := filepath.Join(root, "Shell")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, s := range srvs {
		b.WriteString(knownhosts.Line([]string{s.addr.String()}, s.pubKey))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAppJumpPoolReuseLiveSessionAndIdleEviction(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)

	bastion := startAppTestSSH(t)
	target1 := startAppTestSSH(t)
	target2 := startAppTestSSH(t)
	pinTestKnownHosts(t, root, bastion, target1, target2)

	app := newApp()
	app.jumpIdleTimeout = 80 * time.Millisecond
	defer app.DisconnectAllSessions()

	bProf, err := app.SaveProfileWithSecret(local.Profile{
		Name: "HK-Bastion", Host: "127.0.0.1", Port: bastion.addr.Port, User: "test", Auth: "password",
	}, "pw", true, false)
	if err != nil {
		t.Fatal(err)
	}
	t1Prof, err := app.SaveProfileWithSecret(local.Profile{
		Name: "SG-Target-1", Host: "127.0.0.1", Port: target1.addr.Port, User: "test", Auth: "password", JumpHostID: bProf.ID,
	}, "pw", true, false)
	if err != nil {
		t.Fatal(err)
	}
	t2Prof, err := app.SaveProfileWithSecret(local.Profile{
		Name: "SG-Target-2", Host: "127.0.0.1", Port: target2.addr.Port, User: "test", Auth: "password", JumpHostID: bProf.ID,
	}, "pw", true, false)
	if err != nil {
		t.Fatal(err)
	}

	bp, _, _ := app.getCachedProfileByID(bProf.ID)
	p1, _, _ := app.getCachedProfileByID(t1Prof.ID)
	p2, _, _ := app.getCachedProfileByID(t2Prof.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Two targets behind the same bastion share ONE pooled bastion connection.
	_, e1, err := app.connectHeadlessProfile(ctx, p1)
	if err != nil {
		t.Fatalf("connect target1 failed: %v", err)
	}
	_, e2, err := app.connectHeadlessProfile(ctx, p2)
	if err != nil {
		t.Fatalf("connect target2 failed: %v", err)
	}
	if got := bastion.handshakes.Load(); got != 1 {
		t.Fatalf("expected 1 pooled bastion handshake for 2 jumped targets, got %d", got)
	}

	// Closing child 1 must NOT close the pooled bastion while child 2 is still alive.
	e1.session.Close()
	time.Sleep(110 * time.Millisecond)
	if _, _, err := e2.session.Client().SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatalf("child 2 died while pooled bastion should remain active (refCount>0): %v", err)
	}

	// Closing child 2 drops refCount to 0 -> idle eviction closes the pooled bastion after jumpIdleTimeout.
	e2.session.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		app.jumpMu.Lock()
		n := len(app.jumpPool)
		app.jumpMu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	app.jumpMu.Lock()
	remaining := len(app.jumpPool)
	app.jumpMu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected idle pooled jump to be evicted, still have %d", remaining)
	}

	// 2. When bastion is ALREADY connected as a live/headless session, child reuses its *ssh.Client directly!
	_, bEntry, err := app.connectHeadlessProfile(ctx, bp)
	if err != nil {
		t.Fatalf("connect bastion directly failed: %v", err)
	}
	if got := bastion.handshakes.Load(); got != 2 {
		t.Fatalf("expected 2nd bastion handshake for direct bastion session, got %d", got)
	}
	_, e3, err := app.connectHeadlessProfile(ctx, p1)
	if err != nil {
		t.Fatalf("connect target1 via live bastion failed: %v", err)
	}
	if got := bastion.handshakes.Load(); got != 2 {
		t.Fatalf("expected live bastion session reuse (handshakes=2), got %d", got)
	}
	// Closing child e3 must NOT close the borrowed live bastion session!
	e3.session.Close()
	if bEntry.session.IsClosed() || bEntry.session.Client() == nil {
		t.Fatal("closing jumped child closed the borrowed live bastion session")
	}
	if _, _, err := bEntry.session.Client().SendRequest("keepalive@openssh.com", true, nil); err != nil {
		t.Fatalf("borrowed live bastion session stopped responding after child close: %v", err)
	}
}

func TestShellConnectInstantReuseAndListToolsGuidance(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	app := newApp()

	// Verify ListTools descriptions clearly guide single-call host usage.
	toolMap := make(map[string]string)
	for _, spec := range app.ListTools() {
		toolMap[spec.Name] = strings.ToLower(spec.Description)
	}
	for _, name := range []string{"shell_exec", "shell_sftp_list", "shell_sftp_read", "shell_sftp_write", "shell_server_stats"} {
		desc := toolMap[name]
		if !strings.Contains(desc, "host") || !strings.Contains(desc, "shell_connect") {
			t.Fatalf("tool %s description should clarify single-call host usage: %q", name, desc)
		}
	}

	// Verify shell_connect immediately reuses an existing live session in silent mode without redialing.
	saved, err := app.SaveProfile(local.Profile{Name: "fast-box", Host: "10.1.2.3", Port: 22, User: "root", Auth: "agent", AgentAccess: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	prof, _, _ := app.getCachedProfileByID(saved.ID)
	app.headless["mcp-preconnected"] = &liveSession{
		profile: prof,
		session: &remote.Session{},
	}
	out := app.CallTool(context.Background(), "mcp", "test-client", "shell_connect", map[string]any{"host": "fast-box"})
	if out.IsError {
		t.Fatalf("shell_connect reuse failed: %s", out.Text)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out.Text), &payload); err != nil {
		t.Fatalf("invalid json response: %v (%s)", err, out.Text)
	}
	if payload["sessionId"] != "mcp-preconnected" || payload["reused"] != true {
		t.Fatalf("expected instant reuse of mcp-preconnected, got %+v", payload)
	}
}
