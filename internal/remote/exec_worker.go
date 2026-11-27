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
	closed bool
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

func (w *sshExecWorker) close() {
	if w == nil || w.closed {
		return
	}
	w.closed = true
	_ = w.stdin.Close()
	_ = w.sess.Close()
}

func (w *sshExecWorker) run(ctx context.Context, command string, maxBytes int64) (ExecResult, error) {
	if w == nil || w.closed {
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
	var outBuf, errBuf bytes.Buffer
	var outErr, errErr error
	exitCode := 0

	var wg sync.WaitGroup
	wg.Add(2)

	// Read stdout until stdoutMarker line
	go func() {
		defer wg.Done()
		for {
			line, err := w.stdout.ReadString('\n')
			if idx := strings.Index(line, stdoutMarker); idx >= 0 {
				if idx > 0 && int64(outBuf.Len()) < maxBytes {
					outBuf.WriteString(line[:idx])
				}
				codeStr := strings.TrimSpace(line[idx+len(stdoutMarker):])
				if n, pErr := strconv.Atoi(codeStr); pErr == nil {
					exitCode = n
				}
				return
			}
			if line != "" && int64(outBuf.Len()) < maxBytes {
				remain := int(maxBytes) - outBuf.Len()
				if len(line) > remain {
					outBuf.WriteString(line[:remain])
				} else {
					outBuf.WriteString(line)
				}
			}
			if err != nil {
				outErr = err
				return
			}
		}
	}()

	// Read stderr until stderrMarker line
	go func() {
		defer wg.Done()
		errLimit := maxBytes / 2
		for {
			line, err := w.stderr.ReadString('\n')
			if idx := strings.Index(line, stderrMarker); idx >= 0 {
				if idx > 0 && int64(errBuf.Len()) < errLimit {
					errBuf.WriteString(line[:idx])
				}
				return
			}
			if line != "" && int64(errBuf.Len()) < errLimit {
				remain := int(errLimit) - errBuf.Len()
				if len(line) > remain {
					errBuf.WriteString(line[:remain])
				} else {
					errBuf.WriteString(line)
				}
			}
			if err != nil {
				errErr = err
				return
			}
		}
	}()

	wg.Wait()

	if ctx.Err() != nil {
		w.close()
		return ExecResult{
			Stdout:     strings.TrimSuffix(outBuf.String(), "\n"),
			Stderr:     strings.TrimSuffix(errBuf.String(), "\n"),
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

	// Trim the single sentinel leading newline we injected before the marker.
	stdoutStr := strings.TrimSuffix(outBuf.String(), "\n")
	stderrStr := strings.TrimSuffix(errBuf.String(), "\n")
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
	client, jumps, _, stop, err := dialSSHClient(ctx, setup, cfg, cb, nil, 0)
	if err != nil {
		return nil, err
	}
	initialRTT := time.Since(t0).Milliseconds()

	keepCtx, stopKeep := context.WithCancel(ctx)
	s := &Session{
		client:      client,
		jumpClients: jumps,
		stop:        stop,
		stopKeep:    stopKeep,
		tunnels:     make(map[string]*activeTunnel),
	}
	s.latencyMs.Store(initialRTT)
	if cb.Latency != nil {
		cb.Latency(initialRTT)
	}
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
	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&outBuf, io.LimitReader(stdout, maxBytes))
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&errBuf, io.LimitReader(stderr, maxBytes/2))
	}()
	wg.Wait()

	waitErr := sess.Wait()
	res := ExecResult{
		Stdout:     outBuf.String(),
		Stderr:     errBuf.String(),
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
