package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// Model per-request service latency without contacting any remote host.
type transferProbe struct {
	sftp.FileReader
	sftp.FileWriter
	active, maximum atomic.Int32
	delay           time.Duration
	failChunks      atomic.Bool
}

func (p *transferProbe) begin(offset int64) error {
	active := p.active.Add(1)
	for old := p.maximum.Load(); active > old && !p.maximum.CompareAndSwap(old, active); old = p.maximum.Load() {
	}
	time.Sleep(p.delay)
	if p.failChunks.Load() && offset >= sftpPacketSize*3 {
		return os.ErrPermission
	}
	return nil
}

func (p *transferProbe) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	f, err := p.FileReader.Fileread(r)
	return delayedRead{p, f}, err
}

type delayedRead struct {
	*transferProbe
	io.ReaderAt
}

func (r delayedRead) ReadAt(b []byte, off int64) (int, error) {
	defer r.active.Add(-1)
	if err := r.begin(off); err != nil {
		return 0, err
	}
	return r.ReaderAt.ReadAt(b, off)
}

func (p *transferProbe) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	f, err := p.FileWriter.Filewrite(r)
	return delayedWrite{p, f}, err
}

type delayedWrite struct {
	*transferProbe
	io.WriterAt
}

func (w delayedWrite) WriteAt(b []byte, off int64) (int, error) {
	defer w.active.Add(-1)
	if err := w.begin(off); err != nil {
		return 0, err
	}
	return w.WriterAt.WriteAt(b, off)
}

func transferFixture(t testing.TB) (*Session, *transferProbe, []byte) {
	t.Helper()
	serverSide, clientSide := net.Pipe()
	handlers := sftp.InMemHandler()
	probe := &transferProbe{FileReader: handlers.FileGet, FileWriter: handlers.FilePut, delay: 2 * time.Millisecond}
	handlers.FileGet, handlers.FilePut = probe, probe
	server := sftp.NewRequestServer(serverSide, handlers)
	go server.Serve()
	t.Cleanup(func() { clientSide.Close(); server.Close() })
	c, err := sftp.NewClientPipe(clientSide, clientSide, sftp.UseFstat(true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return &Session{files: c}, probe, data
}

func TestPipelinedTransfersAndProgress(t *testing.T) {
	s, probe, data := transferFixture(t)
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "source"), filepath.Join(dir, "copy")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"upload", "download"} {
		var progress []int64
		probe.maximum.Store(0)
		record := func(done, total int64) {
			if total != int64(len(data)) {
				t.Errorf("total = %d", total)
			}
			progress = append(progress, done)
		}
		var err error
		if dir == "upload" {
			err = s.UploadFile(context.Background(), src, "/file", false, record)
		} else {
			err = s.DownloadFile(context.Background(), "/file", dst, false, record)
		}
		if err != nil {
			t.Fatal(err)
		}
		if probe.maximum.Load() < 2 {
			t.Fatalf("%s only had one packet in flight", dir)
		}
		if len(progress) < 2 || progress[0] != 0 || progress[len(progress)-1] != int64(len(data)) {
			t.Fatalf("%s progress = %v", dir, progress)
		}
		for i := 1; i < len(progress); i++ {
			if progress[i] <= progress[i-1] {
				t.Fatalf("%s repeated or regressed progress: %v", dir, progress)
			}
		}
	}
	got, err := os.ReadFile(dst)
	if err != nil || !bytes.Equal(data, got) {
		t.Fatalf("round trip contents differ, err=%v", err)
	}
}

func TestInterruptedTransfersKeepOriginal(t *testing.T) {
	s, probe, data := transferFixture(t)
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Upload(src, "/source"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteText("/target", "original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"upload", "download"} {
		for _, cause := range []string{"cancel", "I/O error"} {
			t.Run(direction+"/"+cause, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				probe.failChunks.Store(cause == "I/O error")
				defer probe.failChunks.Store(false)
				onProgress := func(done, total int64) {
					if done == total {
						t.Error("reported completion before publishing")
					}
					if done > 0 && cause == "cancel" {
						cancel()
					}
				}
				var err error
				if direction == "upload" {
					err = s.UploadFile(ctx, src, "/target", true, onProgress)
				} else {
					err = s.DownloadFile(ctx, "/source", dst, true, onProgress)
				}
				if err == nil || (cause == "cancel" && !errors.Is(err, context.Canceled)) {
					t.Fatalf("unexpected result: %v", err)
				}
				if remote, err := s.ReadText("/target"); err != nil || remote != "original" {
					t.Fatalf("remote target changed: %v", err)
				}
				if local, err := os.ReadFile(dst); err != nil || string(local) != "original" {
					t.Fatalf("local target changed: %v", err)
				}
				if files, err := s.files.ReadDir("/"); err != nil || len(files) != 2 {
					t.Fatalf("remote temporary file left behind: %v %v", files, err)
				}
				if files, err := os.ReadDir(dir); err != nil || len(files) != 2 {
					t.Fatalf("local temporary file left behind: %v %v", files, err)
				}
			})
		}
	}
}

func BenchmarkSFTPTransfer(b *testing.B) {
	s, _, data := transferFixture(b)
	remote, err := s.files.OpenFile("/input", os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := remote.Write(data); err != nil {
		b.Fatal(err)
	}
	if err := remote.Close(); err != nil {
		b.Fatal(err)
	}
	for _, dir := range []string{"upload", "download"} {
		for _, mode := range []string{"sequential", "pipelined"} {
			b.Run(dir+"/"+mode, func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					p := &transferProgress{ctx: context.Background(), total: int64(len(data))}
					var n int64
					var f *sftp.File
					var err error
					if dir == "upload" {
						f, err = s.files.OpenFile("/output", os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
						if err != nil {
							b.Fatal(err)
						}
						local := bytes.NewReader(data)
						if mode == "pipelined" {
							n, err = p.upload(f, local)
						} else {
							n, err = io.Copy(f, &progressReader{p, local})
						}
					} else {
						f, err = s.files.Open("/input")
						if err != nil {
							b.Fatal(err)
						}
						// Match a regular file's 32 KiB generic copy buffer in the legacy path.
						// Exposing io.Discard.ReadFrom would use its special 8 KiB buffer.
						dst := struct{ io.Writer }{io.Discard}
						if mode == "pipelined" {
							n, err = p.download(dst, f)
						} else {
							n, err = io.Copy(dst, &progressReader{p, f})
						}
					}
					closeErr := f.Close()
					if err != nil || closeErr != nil || n != int64(len(data)) {
						b.Fatalf("bytes=%d, copy=%v, close=%v", n, err, closeErr)
					}
				}
			})
		}
	}
}
