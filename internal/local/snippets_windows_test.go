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
	isolated(t)
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
}

func TestSnippetUpgradeHonorsEditsAndDeletions(t *testing.T) {
	isolated(t)
	old := originalSnippets()[1:] // A user deleted the first bundled command.
	old[0].Name = "renamed disk command"
	custom := Snippet{ID: "custom", Name: "my distro", Category: "custom", Command: "  " + additionalSnippets()[0].Command + "  "}
	old = append(old, custom)
	if err := withStore(func(dir string) error { return writeSnippets(dir, old) }); err != nil {
		t.Fatal(err)
	}
	list, err := Snippets()
	if err != nil || len(list) != len(old)+19 || !slices.Equal(list[:len(old)], old) {
		t.Fatalf("upgrade lost user choices: %+v, %v", list, err)
	}
	for _, s := range list {
		if s.ID == originalSnippets()[0].ID || s.ID == additionalSnippets()[0].ID {
			t.Fatalf("unexpected preset %q", s.ID)
		}
	}
	renamed := list[len(old)]
	renamed.Name = "my CPU details"
	renamed.Command = "lscpu --extended"
	if _, err := SaveSnippet(renamed); err != nil {
		t.Fatal(err)
	}
	deleted := list[len(old)+1]
	if err := DeleteSnippet(deleted.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		list, err = Snippets()
		if err != nil || len(list) != len(old)+18 || !slices.Contains(list, renamed) || slices.Contains(list, deleted) {
			t.Fatalf("edited or deleted presets changed on reopen: %+v, %v", list, err)
		}
	}
}

func TestUpgradeNearOriginalSnippetLimit(t *testing.T) {
	dir := isolated(t)
	const oldLimit = 2 << 20
	custom := Snippet{ID: "custom", Name: "long description", Category: "custom", Command: "ls", Description: strings.Repeat("x", oldLimit-256)}
	if err := withStore(func(dir string) error { return writeSnippets(dir, []Snippet{custom}) }); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		list, err := Snippets()
		if err != nil || len(list) != 21 || list[0] != custom {
			t.Fatalf("near-limit legacy catalog: %d snippets, %v", len(list), err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "snippets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []Snippet
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 21 {
		t.Fatalf("legacy JSON array compatibility: %d rows, %v", len(rows), err)
	}
}
