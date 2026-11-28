package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFreshSnippetCatalog(t *testing.T) {
	dir := isolated(t)
	list, err := Snippets()
	if err != nil || len(list) != 28 {
		t.Fatalf("new install: %d snippets, %v", len(list), err)
	}
	ids := map[string]bool{}
	commands := map[string]bool{}
	for _, s := range list {
		if _, err := normalizeSnippet(s); err != nil || s.ID == "" || ids[s.ID] || commands[s.Command] {
			t.Fatalf("invalid or duplicate preset: %+v, %v", s, err)
		}
		ids[s.ID] = true
		commands[s.Command] = true
	}
	again, err := Snippets()
	if err != nil || !slices.Equal(list, again) {
		t.Fatalf("second read changed catalog: %v", err)
	}
	// Stored cleanly under config/snippets.json with zero legacy catalog marker file.
	data, err := os.ReadFile(filepath.Join(dir, "config", "snippets.json"))
	if err != nil {
		t.Fatalf("missing config/snippets.json: %v", err)
	}
	var rows []Snippet
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 28 {
		t.Fatalf("unexpected persisted snippets: %d rows, %v", len(rows), err)
	}
	for _, legacy := range []string{
		filepath.Join(dir, "snippets.json"),
		filepath.Join(dir, "snippets-catalog.version"),
		filepath.Join(dir, "config", "snippets-catalog.version"),
	} {
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("unexpected legacy snippet file %s", legacy)
		}
	}
}

func TestSnippetEditsAndDeletionsPersistCleanly(t *testing.T) {
	isolated(t)
	list, err := Snippets()
	if err != nil || len(list) != 28 {
		t.Fatalf("initial snippets: %d, %v", len(list), err)
	}

	edited := list[0]
	edited.Name = "自定义系统概览"
	edited.Command = "uptime && free -m"
	if _, err := SaveSnippet(edited); err != nil {
		t.Fatal(err)
	}

	deleted := list[1]
	if err := DeleteSnippet(deleted.ID); err != nil {
		t.Fatal(err)
	}

	added, err := SaveSnippet(Snippet{Name: "自定义检查", Category: "网络", Command: "curl -I https://example.com"})
	if err != nil || added.ID == "" {
		t.Fatalf("SaveSnippet new item failed: %+v, %v", added, err)
	}

	for range 2 {
		reloaded, err := Snippets()
		if err != nil || len(reloaded) != 28 {
			t.Fatalf("reloaded snippets: %d, %v", len(reloaded), err)
		}
		if !slices.Contains(reloaded, edited) {
			t.Fatalf("edited preset lost on reload")
		}
		if slices.Contains(reloaded, deleted) {
			t.Fatalf("deleted preset unexpectedly re-added on reload")
		}
		if !slices.Contains(reloaded, added) {
			t.Fatalf("custom snippet lost on reload")
		}
	}
}

func TestSnippetsCompactWriteNearLimit(t *testing.T) {
	dir := isolated(t)
	custom := Snippet{ID: "custom", Name: "long description", Category: "custom", Command: "ls", Description: strings.Repeat("x", snippetsLimit-256)}
	if err := withStore(func(dir string) error { return writeSnippets(dir, []Snippet{custom}) }); err != nil {
		t.Fatal(err)
	}
	list, err := Snippets()
	if err != nil || len(list) != 1 || list[0] != custom {
		t.Fatalf("near-limit catalog mismatch: %d snippets, %v", len(list), err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config", "snippets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []Snippet
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("unexpected snippets.json content: %d rows, %v", len(rows), err)
	}
}
