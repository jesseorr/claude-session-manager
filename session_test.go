package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenamePreservesMtimeAndWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abc-123.jsonl")
	body := `{"type":"user","cwd":"/home/x/proj","gitBranch":"main","timestamp":"2026-08-18T17:10:00.000Z"}
{"type":"ai-title","aiTitle":"old title","sessionId":"abc-123"}
{"type":"last-prompt","lastPrompt":"where did I leave off","sessionId":"abc-123"}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	os.Chtimes(path, want, want)

	s := scanFile(path)
	if s.Title != "old title" || s.Turns != 1 || s.Cwd != "/home/x/proj" || s.Branch != "main" {
		t.Fatalf("scan: %+v", s)
	}
	if s.Last != "where did I leave off" {
		t.Fatalf("last prompt: %q", s.Last)
	}

	if err := rename(s, `new "quoted" title`); err != nil {
		t.Fatal(err)
	}
	if got := scanFile(path); got.Title != `new "quoted" title` {
		t.Fatalf("title after rename: %q", got.Title)
	}
	fi, _ := os.Stat(path)
	if !fi.ModTime().Truncate(time.Second).Equal(want) {
		t.Fatalf("mtime moved: %v want %v", fi.ModTime(), want)
	}
}

func TestScanFallsBackToEncodedDirName(t *testing.T) {
	// A transcript with no cwd recorded in it falls back to decoding the
	// project directory name. That name replaced every "/" with "-", so
	// "one-offs" and "one/offs" encode identically and only the real tree can
	// tell them apart. Build that tree here rather than relying on the
	// machine running the test to happen to have one.
	base := t.TempDir()
	realCwd := filepath.Join(base, "one-offs", "loki")
	if err := os.MkdirAll(realCwd, 0o755); err != nil {
		t.Fatal(err)
	}

	encoded := strings.ReplaceAll(realCwd, string(filepath.Separator), "-")
	dir := filepath.Join(base, "projects", encoded)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "z.jsonl")
	os.WriteFile(path, []byte("{\"type\":\"summary\"}\n"), 0o644)

	s := scanFile(path)
	if s.Cwd != realCwd {
		t.Fatalf("cwd fallback: %q, want %q", s.Cwd, realCwd)
	}
	if s.Start.IsZero() || !s.Start.Equal(s.End) {
		t.Fatalf("start should fall back to mtime: %v %v", s.Start, s.End)
	}
}
