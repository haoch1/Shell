package remote

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

type replaceTestCommands struct {
	sftp.FileCmder
	posixErr   error
	failCommit bool
}

func (cmd replaceTestCommands) PosixRename(*sftp.Request) error { return cmd.posixErr }
func (cmd replaceTestCommands) Filecmd(r *sftp.Request) error {
	if cmd.failCommit && r.Method == "Rename" && r.Target == "/original" && !strings.HasSuffix(r.Filepath, ".old") {
		return os.ErrPermission
	}
	return cmd.FileCmder.Filecmd(r)
}

func TestOverwriteKeepsOriginalOnFailure(t *testing.T) {
	for _, op := range []string{"upload", "edit"} {
		for _, tc := range []struct {
			name       string
			posixErr   error
			failCommit bool
			wantError  bool
		}{
			{"permission-denied", os.ErrPermission, false, true},
			{"no-extension-rollback", sftp.ErrSSHFxOpUnsupported, true, true},
			{"no-extension-success", sftp.ErrSSHFxOpUnsupported, false, false},
		} {
			t.Run(op+"/"+tc.name, func(t *testing.T) {
				serverPipe, clientPipe := net.Pipe()
				handlers := sftp.InMemHandler()
				handlers.FileCmd = replaceTestCommands{handlers.FileCmd, tc.posixErr, tc.failCommit}
				server := sftp.NewRequestServer(serverPipe, handlers)
				go server.Serve()
				t.Cleanup(func() { clientPipe.Close(); server.Close() })
				c, err := sftp.NewClientPipe(clientPipe, clientPipe)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				file, err := c.Create("/original")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = io.WriteString(file, "old"); err != nil {
					t.Fatal(err)
				}
				if err = file.Close(); err != nil {
					t.Fatal(err)
				}
				session := &Session{files: c}
				if op == "edit" {
					err = session.WriteText("/original", "new")
				} else {
					source := filepath.Join(t.TempDir(), "source")
					if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
						t.Fatal(err)
					}
					err = session.UploadFile(context.Background(), source, "/original", true, nil)
				}
				if (err != nil) != tc.wantError {
					t.Fatalf("publish error = %v", err)
				}
				got, err := session.ReadText("/original")
				want := "new"
				if tc.wantError {
					want = "old"
				}
				if err != nil || got != want {
					t.Fatalf("published file = %q, err = %v; want %q", got, err, want)
				}
				entries, err := c.ReadDir("/")
				if err != nil || len(entries) != 1 || entries[0].Name() != "original" {
					t.Fatalf("directory after publish: %v, %v", entries, err)
				}
			})
		}
	}
}
