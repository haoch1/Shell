package security

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"
)

// AuditEntry records a single Agent or MCP tool invocation for compliance & traceability.
type AuditEntry struct {
	ID            string       `json:"id"`
	Timestamp     int64        `json:"timestamp"`
	Source        string       `json:"source"`     // "mcp", "http", "builtin-ai"
	ClientName    string       `json:"clientName"` // e.g. "Antigravity", "Claude Code", "Shell AI"
	Host          string       `json:"host"`
	SessionID     string       `json:"sessionId,omitempty"`
	Tool          string       `json:"tool"`
	Summary       string       `json:"summary"`
	Risk          string       `json:"risk"`     // "safe", "caution", "danger"
	Decision      string       `json:"decision"` // "auto-allow", "user-allow", "session-allow", "user-deny", "policy-deny"
	RedactedCount int          `json:"redactedCount"`
	DurationMs    int64        `json:"durationMs"`
	Phases        *AuditPhases `json:"phases,omitempty"`
	OK            bool         `json:"ok"`
	Error         string       `json:"error,omitempty"`
}

// Nil marks a phase that was never entered. Zero means a measured phase below one millisecond.
type AuditPhases struct {
	SessionMs  *int64 `json:"sessionMs,omitempty"`
	ApprovalMs *int64 `json:"approvalMs,omitempty"`
	RemoteMs   *int64 `json:"remoteMs,omitempty"`
}

const (
	maxAuditBytes   = 4 << 20
	maxAuditEntries = 1500
)

var auditMu sync.Mutex

func auditFilePath() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "Shell")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "audit.jsonl"), nil
}

func newAuditID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// AppendAudit appends one redacted audit entry to %LOCALAPPDATA%\Shell\audit.jsonl.
func AppendAudit(e AuditEntry) error {
	if e.ID == "" {
		e.ID = newAuditID()
	}
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().Unix()
	}
	// Always scrub secrets from audit summary & error before persisting to disk.
	redCfg := DefaultRedactionConfig()
	e.Summary = Redact(e.Summary, redCfg).Text
	if e.Error != "" {
		e.Error = Redact(e.Error, redCfg).Text
	}
	if len(e.Summary) > 512 {
		n := 512
		for n > 0 && !utf8.RuneStart(e.Summary[n]) {
			n--
		}
		e.Summary = e.Summary[:n] + "…"
	}

	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	auditMu.Lock()
	defer auditMu.Unlock()

	p, err := auditFilePath()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, wErr := f.Write(line)
	cErr := f.Close()
	if wErr != nil {
		return wErr
	}
	if cErr != nil {
		return cErr
	}

	// Compact if file grew past limit
	if info, err := os.Stat(p); err == nil && info.Size() > maxAuditBytes {
		compactAuditLocked(p)
	}
	return nil
}

func compactAuditLocked(p string) {
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) <= maxAuditEntries {
		return
	}
	kept := lines[len(lines)-maxAuditEntries:]
	out := append(bytes.Join(kept, []byte("\n")), '\n')
	tmp, err := os.CreateTemp(filepath.Dir(p), ".audit-*.tmp")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	_, wErr := tmp.Write(out)
	if wErr == nil {
		wErr = tmp.Sync()
	}
	cErr := tmp.Close()
	if wErr != nil || cErr != nil {
		_ = os.Remove(tmpName)
		return
	}
	if os.Rename(tmpName, p) != nil {
		_ = os.Remove(tmpName)
	}
}

// ListAudit returns up to `limit` most recent audit entries (newest first).
func ListAudit(limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > maxAuditEntries {
		limit = 200
	}
	auditMu.Lock()
	defer auditMu.Unlock()

	p, err := auditFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return []AuditEntry{}, nil
	}
	if err != nil {
		return nil, err
	}

	var all []AuditEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 16<<10), 256<<10)
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var e AuditEntry
		if json.Unmarshal(raw, &e) == nil {
			all = append(all, e)
		}
	}
	// Reverse to newest-first and cap to limit
	n := len(all)
	if n > limit {
		all = all[n-limit:]
	}
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if all == nil {
		all = []AuditEntry{}
	}
	return all, nil
}

// ClearAudit empties the local audit log file.
func ClearAudit() error {
	auditMu.Lock()
	defer auditMu.Unlock()
	p, err := auditFilePath()
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
