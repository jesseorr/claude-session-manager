package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompletePath(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"alpha", "alpine", "beta", ".hidden"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "alphafile"), nil, 0o644)
	os.MkdirAll(filepath.Join(root, "beta", "nested"), 0o755)

	cases := []struct{ in, want string }{
		{filepath.Join(root, "b"), filepath.Join(root, "beta") + "/"}, // one match completes fully
		{filepath.Join(root, "al"), filepath.Join(root, "alp")},       // several stop at the common prefix
		{filepath.Join(root, "zz"), filepath.Join(root, "zz")},        // no match is left alone
		{root + "/.", root + "/.hidden/"},                             // a typed dot reaches hidden dirs
	}
	for _, c := range cases {
		if got := completePath(c.in); got != c.want {
			t.Errorf("completePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCompletePathIgnoresFiles(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "notes.md"), nil, 0o644)
	in := filepath.Join(root, "not")
	if got := completePath(in); got != in {
		t.Fatalf("completed to a regular file: %q", got)
	}
}

func TestExpandPath(t *testing.T) {
	if got := expandPath("~"); got != home {
		t.Errorf("~ = %q", got)
	}
	if got, want := expandPath("~/projects/x"), filepath.Join(home, "projects/x"); got != want {
		t.Errorf("~/projects/x = %q, want %q", got, want)
	}
	if got := expandPath("/etc/hosts"); got != "/etc/hosts" {
		t.Errorf("absolute path rewritten: %q", got)
	}
}

func TestSuggestDirsRanksRecentThenCommon(t *testing.T) {
	now := time.Now()
	at := func(min int) time.Time { return now.Add(-time.Duration(min) * time.Minute) }
	// collect() hands sessions over newest first, which is the order this relies on.
	sessions := []*session{
		{Cwd: "/a", End: at(1)},
		{Cwd: "/b", End: at(2)},
		{Cwd: "/c", End: at(3)},
		{Cwd: "/d", End: at(4)},
		{Cwd: "/e", End: at(5)},
		{Cwd: "/busy", End: at(6)},
		{Cwd: "/busy", End: at(7)},
		{Cwd: "/busy", End: at(8)},
		{Cwd: "/once", End: at(9)},
	}

	got := suggestDirs(sessions)
	if len(got) != 6 {
		t.Fatalf("want 5 recent + 1 common, got %+v", got)
	}
	for i, want := range []string{"/a", "/b", "/c", "/d", "/e"} {
		if got[i].Path != want || got[i].Why != "recent" {
			t.Fatalf("slot %d = %+v, want %s recent", i, got[i], want)
		}
	}
	if got[5].Path != "/busy" || got[5].Why != "3 sessions" {
		t.Fatalf("common slot = %+v", got[5])
	}
	// A directory used once adds nothing over the recent list.
	for _, sg := range got {
		if sg.Path == "/once" {
			t.Fatal("/once should not be suggested as common")
		}
	}
}

func TestSuggestDirsSkipsRepeatsAcrossSections(t *testing.T) {
	now := time.Now()
	sessions := []*session{
		{Cwd: "/hot", End: now.Add(-1 * time.Minute)},
		{Cwd: "/hot", End: now.Add(-2 * time.Minute)},
		{Cwd: "/hot", End: now.Add(-3 * time.Minute)},
	}
	got := suggestDirs(sessions)
	if len(got) != 1 || got[0].Path != "/hot" || got[0].Why != "recent" {
		t.Fatalf("a recent directory should not be listed twice: %+v", got)
	}
}
