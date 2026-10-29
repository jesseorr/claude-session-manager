package main

import (
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTempStore points the package-level paths at a scratch directory so no
// test can touch the real ~/.claude.
func withTempStore(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	oldProjects, oldTrash, oldPins, oldPrefs := projectsDir, trashDir, pinsFile, prefsFile
	projectsDir = filepath.Join(root, "projects")
	trashDir = filepath.Join(root, "trash")
	pinsFile = filepath.Join(root, "pins.json")
	prefsFile = filepath.Join(root, "prefs.json")
	t.Cleanup(func() {
		projectsDir, trashDir, pinsFile, prefsFile = oldProjects, oldTrash, oldPins, oldPrefs
	})
	return root
}

func writeSession(t *testing.T, project, id string, lines ...string) *session {
	t.Helper()
	dir := filepath.Join(projectsDir, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return scanFile(path)
}

func TestDiscardAndRestoreRoundTrip(t *testing.T) {
	withTempStore(t)
	s := writeSession(t, "-tmp-scratch", "aaaa1111",
		`{"type":"user","cwd":"/tmp/scratch","timestamp":"2026-08-18T17:10:00.000Z"}`,
		`{"type":"last-prompt","lastPrompt":"throwaway","sessionId":"aaaa1111"}`)

	// Subagent transcripts live in a directory named for the session.
	subDir := filepath.Join(projectsDir, "-tmp-scratch", "aaaa1111")
	os.MkdirAll(subDir, 0o755)
	os.WriteFile(filepath.Join(subDir, "agent-x.jsonl"), []byte("{}\n"), 0o644)

	if err := discard(s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.File); !os.IsNotExist(err) {
		t.Fatal("transcript still in place after discard")
	}
	if _, err := os.Stat(subDir); !os.IsNotExist(err) {
		t.Fatal("subagent directory left behind")
	}
	if got := listTrash(); len(got) != 1 || got[0].ID != "aaaa1111" {
		t.Fatalf("trash listing: %+v", got)
	}

	if err := restore("aaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.File); err != nil {
		t.Fatalf("transcript not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(subDir, "agent-x.jsonl")); err != nil {
		t.Fatalf("subagent transcript not restored: %v", err)
	}
	if len(listTrash()) != 0 {
		t.Fatal("trash should be empty after restore")
	}
}

func TestRestoreRejectsAmbiguousPrefix(t *testing.T) {
	withTempStore(t)
	for _, id := range []string{"dupe0001", "dupe0002"} {
		s := writeSession(t, "-tmp-x", id, `{"type":"last-prompt","lastPrompt":"x"}`)
		if err := discard(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := restore("dupe"); err == nil {
		t.Fatal("ambiguous prefix should not restore anything")
	}
	if err := restore("dupe0001"); err != nil {
		t.Fatal(err)
	}
	if len(listTrash()) != 1 {
		t.Fatal("only the named session should have been restored")
	}
}

func TestPruneCandidatesSkipsPinnedAndLive(t *testing.T) {
	sessions := []*session{
		{ID: "tmp", Cwd: "/tmp/foo", Turns: 40},
		{ID: "short", Cwd: "/work/keepme", Turns: 1},
		{ID: "keep", Cwd: "/work/keepme", Turns: 90},
		{ID: "pinned", Cwd: "/tmp/bar", Turns: 1},
		{ID: "running", Cwd: "/tmp/baz", Turns: 1, Live: true},
		{ID: "tmpish", Cwd: "/tmpfoo", Turns: 50},
	}
	got := map[string]bool{}
	for _, s := range pruneCandidates(sessions, map[string]bool{"pinned": true}, 3) {
		got[s.ID] = true
	}
	want := map[string]bool{"tmp": true, "short": true}
	if len(got) != len(want) {
		t.Fatalf("candidates: %v", got)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("missing %q, got %v", id, got)
		}
	}
}

func TestPinsRoundTrip(t *testing.T) {
	withTempStore(t)
	if err := savePins(map[string]bool{"a": true, "b": false, "c": true}); err != nil {
		t.Fatal(err)
	}
	got := loadPins()
	if len(got) != 2 || !got["a"] || !got["c"] || got["b"] {
		t.Fatalf("pins: %v", got)
	}
}

func TestFileStatsCountsDiffLines(t *testing.T) {
	withTempStore(t)
	edit := `{"type":"user","toolUseResult":{"filePath":"/p/main.go","structuredPatch":[{"lines":["+add one","+add two","-drop one"," context"]}]}}`
	again := `{"type":"user","toolUseResult":{"filePath":"/p/main.go","structuredPatch":[{"lines":["+third"]}]}}`
	create := `{"type":"user","toolUseResult":{"filePath":"/p/new.txt","content":"a\nb","structuredPatch":[]}}`
	s := writeSession(t, "-p", "stats001", edit, again, create)

	got := fileStats(s.File)
	if len(got) != 2 {
		t.Fatalf("want 2 files, got %+v", got)
	}
	if got[0].Path != "/p/main.go" || got[0].Plus != 3 || got[0].Minus != 1 {
		t.Fatalf("main.go: %+v", got[0])
	}
	if got[1].Path != "/p/new.txt" || got[1].Plus != 2 || got[1].Minus != 0 {
		t.Fatalf("new.txt: %+v", got[1])
	}
}

func TestTargetsResolvesMarksAgainstTheVisibleList(t *testing.T) {
	live := []*session{{ID: "live1"}, {ID: "live2"}}
	trashed := []*session{{ID: "gone1"}, {ID: "gone2"}}

	// A mark made in the trash names a session that is absent from the live
	// list by construction, so resolving against the wrong slice silently
	// yields no targets and the trash verbs do nothing.
	m := model{all: live, trashAll: trashed, trashView: true, shown: trashed,
		marked: map[string]bool{"gone1": true}}
	got := m.targets()
	if len(got) != 1 || got[0].ID != "gone1" {
		t.Fatalf("marked trash row did not resolve: %+v", got)
	}

	m = model{all: live, trashAll: trashed, shown: live,
		marked: map[string]bool{"live2": true}}
	if got := m.targets(); len(got) != 1 || got[0].ID != "live2" {
		t.Fatalf("marked timeline row did not resolve: %+v", got)
	}
}

func TestCloseSessionRefusesAPidItCannotConfirm(t *testing.T) {
	if err := closeSession(&session{Pid: 0}); err == nil {
		t.Fatal("a session with no pid must not be signalled")
	}
	// A record carrying a negative pid would reach syscall.Kill(-1, …), which
	// signals every process the user owns.
	if err := closeSession(&session{Pid: -1}); err == nil {
		t.Fatal("a negative pid must not be signalled")
	}
}

func TestLiveRecordNeedsBothStartTimes(t *testing.T) {
	if !hasProc {
		t.Skip("liveness needs /proc")
	}
	dir := t.TempDir()
	old := liveDir
	liveDir = dir
	t.Cleanup(func() { liveDir = old })

	// Our own pid is certainly running, but a record that omits procStart
	// cannot prove which process it meant.
	write := func(name, body string) {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	write("a.json", fmt.Sprintf(`{"pid":%d,"sessionId":"noproof"}`, os.Getpid()))
	write("b.json", fmt.Sprintf(`{"pid":%d,"sessionId":"proven","procStart":%q}`,
		os.Getpid(), procStart(os.Getpid())))

	got := liveRecords()
	if got["noproof"].Alive {
		t.Error("a record with no procStart must not count as alive")
	}
	if !got["proven"].Alive {
		t.Error("a record whose procStart matches must count as alive")
	}
}

func TestAncestorSessionWalksUpThePidChain(t *testing.T) {
	if !hasProc {
		t.Skip("needs /proc")
	}
	if got := procParent(os.Getpid()); got != os.Getppid() {
		t.Fatalf("procParent: got %d, want %d", got, os.Getppid())
	}
	// The test binary's parent stands in for claude; the walk starts from us,
	// one level below it, the way `! cs pin` starts below its shell.
	live := map[string]liveRecord{
		"mine":  {Pid: os.Getppid(), Alive: true},
		"stale": {Pid: os.Getpid(), Alive: false},
	}
	if got := ancestorSession(os.Getpid(), live); got != "mine" {
		t.Fatalf("got %q, want mine", got)
	}
	if got := ancestorSession(os.Getpid(), map[string]liveRecord{}); got != "" {
		t.Fatalf("no records should match nothing, got %q", got)
	}
}

func TestOpenCommands(t *testing.T) {
	s := &session{ID: "abc-123", Cwd: "/work/api", Title: "Trace the retry loop"}
	want := "workspace create --cwd /work/api --label api --no-focus"
	if got := strings.Join(herdrArgs(s, map[string]string{}), " "); got != want {
		t.Fatalf("herdr new space: %q", got)
	}
	spaces, err := parseSpaces([]byte(`{"result":{"type":"workspace_list","workspaces":[{"workspace_id":"w3","label":"api"}]}}`))
	if err != nil || spaces["api"] != "w3" {
		t.Fatalf("parseSpaces: %v %v", spaces, err)
	}
	want = "tab create --workspace w3 --cwd /work/api --label Trace the retry loop --no-focus"
	if got := strings.Join(herdrArgs(s, spaces), " "); got != want {
		t.Fatalf("herdr existing space: %q", got)
	}
	want = "--working-directory=/work/api -e claude --resume abc-123"
	if got := strings.Join(ghosttyArgs(s), " "); got != want {
		t.Fatalf("ghostty: %q", got)
	}
	c, err := parseCreated([]byte(`{"result":{"workspace":{"workspace_id":"w5"},"tab":{"tab_id":"w5:t1"},"root_pane":{"pane_id":"w5:p1"}}}`))
	if err != nil || c != (created{"w5", "w5:t1", "w5:p1"}) {
		t.Fatalf("parseCreated: %+v %v", c, err)
	}
	if _, err := parseCreated([]byte(`{"result":{}}`)); err == nil {
		t.Fatal("missing pane id must be an error")
	}
}

func TestPanesKeepTheirCursorsAndLineUp(t *testing.T) {
	now := time.Now()
	all := []*session{
		{ID: "a", Title: "alpha", End: now, Turns: 5},
		{ID: "b", Title: "beta", End: now.Add(-time.Hour), Turns: 5},
		{ID: "old-pin", Title: "pinned long ago", End: now.AddDate(0, 0, -60), Turns: 5},
	}
	m := model{all: all, days: 7, w: 120, h: 20, pins: map[string]bool{"old-pin": true},
		marked: map[string]bool{}, files: map[string][]fileEdit{}}
	m.apply()
	if len(m.shown) != 2 {
		t.Fatalf("timeline should respect the window: %v", m.shown)
	}
	m.cursor = 1
	m.focus(true)
	if !m.pinsOnly || len(m.shown) != 1 || m.shown[0].ID != "old-pin" || m.cursor != 0 {
		t.Fatalf("bookmarks pane: pinsOnly=%v cursor=%d shown=%v", m.pinsOnly, m.cursor, m.shown)
	}
	m.focus(false)
	if m.pinsOnly || m.cursor != 1 {
		t.Fatalf("timeline cursor not kept: %d", m.cursor)
	}

	rows := strings.Split(m.View(), "\n")
	for _, r := range rows[:len(rows)-1] {
		if w := ansi.StringWidth(r); w > m.w {
			t.Fatalf("row %d wide: %q", w, r)
		}
	}
	at := strings.Index(rows[1], "│")
	if at < 0 || !strings.Contains(strings.Join(rows, "\n"), "pinned") {
		t.Fatalf("split not drawn:\n%s", strings.Join(rows, "\n"))
	}
}

func TestGroupByProjectKeepsGroupsContiguousAndSorted(t *testing.T) {
	now := time.Now()
	all := []*session{
		{ID: "a1", Cwd: "/a", Bytes: 10, End: now, Turns: 1},
		{ID: "b1", Cwd: "/b", Bytes: 50, End: now, Turns: 1},
		{ID: "a2", Cwd: "/a", Bytes: 30, End: now, Turns: 1},
		{ID: "b2", Cwd: "/b", Bytes: 5, End: now, Turns: 1},
	}
	m := model{all: all, sortBy: sortSize, groupBy: groupProject, pins: map[string]bool{},
		marked: map[string]bool{}, files: map[string][]fileEdit{}}
	m.apply()
	var got []string
	for _, s := range m.shown {
		got = append(got, s.ID)
	}
	if strings.Join(got, " ") != "b1 b2 a2 a1" {
		t.Fatalf("desc: %v", got)
	}
	m.asc = true
	m.apply()
	got = got[:0]
	for _, s := range m.shown {
		got = append(got, s.ID)
	}
	if strings.Join(got, " ") != "b2 b1 a1 a2" {
		t.Fatalf("asc: %v", got)
	}
}
