package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	trashDir  = filepath.Join(home, ".claude", ".cs-trash")
	pinsFile  = filepath.Join(home, ".claude", "cs-pins.json")
	prefsFile = filepath.Join(home, ".claude", "cs-prefs.json")
)

// closeSession stops a running session but leaves its transcript alone, so it
// still shows in the list and stays resumable. A session that ignores SIGTERM
// gets SIGKILL, because "close" that leaves the process running is the bug
// this is here to avoid. The stale record is then swept, so the session stops
// being reported as running by anything that reads ~/.claude/sessions.
func closeSession(s *session) error {
	if s.Pid <= 0 {
		sweepRecord(s)
		return fmt.Errorf("not running")
	}
	if !hasProc {
		return fmt.Errorf("closing a session needs /proc, which this system does not have")
	}

	// Confirm this pid is still the process the record claimed before signalling
	// it. Between the last liveness tick and this keypress the session can exit
	// and the pid be reused, and SIGKILL to the wrong process is unrecoverable.
	start := procStart(s.Pid)
	if start == "" || (s.ProcStart != "" && start != s.ProcStart) {
		s.Pid, s.Live = 0, false
		sweepRecord(s)
		return fmt.Errorf("session already exited")
	}
	if err := syscall.Kill(s.Pid, syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 40; i++ {
		if procStart(s.Pid) != start {
			s.Pid, s.Live = 0, false
			sweepRecord(s)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	syscall.Kill(s.Pid, syscall.SIGKILL)
	for i := 0; i < 20; i++ {
		if procStart(s.Pid) != start {
			s.Pid, s.Live = 0, false
			sweepRecord(s)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("pid %d survived SIGKILL", s.Pid)
}

// sweepRecord deletes the ~/.claude/sessions entry once its process is gone.
// Those leftovers are what keep a dead session listed as live.
func sweepRecord(s *session) {
	if s.Record == "" || s.Pid != 0 {
		return
	}
	os.Remove(s.Record)
	s.Record, s.Status = "", ""
}

// gcRecords removes every session record whose process is no longer running,
// and reports how many it cleared. Without /proc there is no way to know which
// those are, so it clears nothing rather than guessing.
func gcRecords() int {
	if !hasProc {
		return 0
	}
	n := 0
	for _, rec := range liveRecords() {
		if !rec.Alive {
			if os.Remove(rec.File) == nil {
				n++
			}
		}
	}
	return n
}

// discard stops the session and moves its transcript into the trash directory,
// keeping the project subdirectory so restore can put it back. Subagent
// transcripts live in a sibling directory named for the session and move too.
func discard(s *session) error {
	if s.Live {
		closeSession(s)
	}
	sweepRecord(s)
	project := filepath.Base(filepath.Dir(s.File))
	dest := filepath.Join(trashDir, project)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if err := os.Rename(s.File, filepath.Join(dest, filepath.Base(s.File))); err != nil {
		return err
	}
	sub := strings.TrimSuffix(s.File, ".jsonl")
	if _, err := os.Stat(sub); err == nil {
		os.Rename(sub, filepath.Join(dest, filepath.Base(sub)))
	}
	return nil
}

type trashed struct {
	ID, Project, File string
}

func listTrash() []trashed {
	var out []trashed
	projects, _ := os.ReadDir(trashDir)
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(trashDir, p.Name()))
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".jsonl") {
				out = append(out, trashed{
					ID:      strings.TrimSuffix(f.Name(), ".jsonl"),
					Project: p.Name(),
					File:    filepath.Join(trashDir, p.Name(), f.Name()),
				})
			}
		}
	}
	return out
}

// restore moves a trashed session back, matching on any unique id prefix.
func restore(prefix string) error {
	var hits []trashed
	for _, t := range listTrash() {
		if strings.HasPrefix(t.ID, prefix) {
			hits = append(hits, t)
		}
	}
	if len(hits) == 0 {
		return fmt.Errorf("no trashed session matching %q", prefix)
	}
	if len(hits) > 1 {
		return fmt.Errorf("%q matches %d sessions; use a longer prefix", prefix, len(hits))
	}

	t := hits[0]
	dest := filepath.Join(projectsDir, t.Project)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if err := os.Rename(t.File, filepath.Join(dest, t.ID+".jsonl")); err != nil {
		return err
	}
	sub := strings.TrimSuffix(t.File, ".jsonl")
	if _, err := os.Stat(sub); err == nil {
		os.Rename(sub, filepath.Join(dest, t.ID))
	}
	return nil
}

// prefs are the view settings the w, h and s keys change, kept so the list opens
// the way it was left.
type prefs struct {
	Days  int  `json:"days"`
	Here  bool `json:"here"`
	Sort  int  `json:"sort"`
	Group int  `json:"group"`
	Asc   bool `json:"asc"`
}

func loadPrefs() prefs {
	p := prefs{Days: 7}
	if b, err := os.ReadFile(prefsFile); err == nil {
		json.Unmarshal(b, &p)
	}
	return p
}

func savePrefs(p prefs) error {
	b, _ := json.Marshal(p)
	return os.WriteFile(prefsFile, b, 0o644)
}

// humanSize keeps the column narrow; transcripts run from bytes to hundreds of
// megabytes and the exact figure never matters.
func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func loadPins() map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(pinsFile)
	if err != nil {
		return out
	}
	var ids []string
	json.Unmarshal(b, &ids)
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func savePins(pins map[string]bool) error {
	ids := make([]string, 0, len(pins))
	for id, on := range pins {
		if on {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	b, _ := json.Marshal(ids)
	return os.WriteFile(pinsFile, b, 0o644)
}

// pruneCandidates picks the sessions that are usually noise: throwaway work in
// a temp directory, and sessions too short to have gone anywhere. Pinned and
// still-running sessions are never candidates.
func pruneCandidates(sessions []*session, pins map[string]bool, minTurns int) []*session {
	var out []*session
	for _, s := range sessions {
		if pins[s.ID] || s.Live {
			continue
		}
		if s.Turns < minTurns || strings.HasPrefix(s.Cwd, "/tmp/") || s.Cwd == "/tmp" {
			out = append(out, s)
		}
	}
	return out
}

type fileEdit struct {
	Path        string
	Plus, Minus int
}

// fileStats totals the line changes each file received, newest edits included,
// by reading the diffs Claude Code records alongside every Edit result. It is
// called lazily for the highlighted session because these lines are large.
func fileStats(path string) []fileEdit {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	totals := map[string]*fileEdit{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, `"structuredPatch"`) {
			continue
		}
		var rec struct {
			ToolUseResult struct {
				FilePath        string `json:"filePath"`
				Content         string `json:"content"`
				StructuredPatch []struct {
					Lines []string `json:"lines"`
				} `json:"structuredPatch"`
			} `json:"toolUseResult"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.ToolUseResult.FilePath == "" {
			continue
		}

		r := rec.ToolUseResult
		e := totals[r.FilePath]
		if e == nil {
			e = &fileEdit{Path: r.FilePath}
			totals[r.FilePath] = e
		}
		if len(r.StructuredPatch) == 0 {
			// A Write of a new file records no diff, just the content.
			if r.Content != "" {
				e.Plus += strings.Count(r.Content, "\n") + 1
			}
			continue
		}
		for _, h := range r.StructuredPatch {
			for _, l := range h.Lines {
				switch {
				case strings.HasPrefix(l, "+"):
					e.Plus++
				case strings.HasPrefix(l, "-"):
					e.Minus++
				}
			}
		}
	}

	out := make([]fileEdit, 0, len(totals))
	for _, e := range totals {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Plus+out[i].Minus, out[j].Plus+out[j].Minus
		if a != b {
			return a > b
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// searchTranscripts reports which sessions mention a phrase anywhere in their
// transcript, not just in the title or the last prompt. It is a plain
// case-insensitive substring scan over ~500MB, so the files are read in
// parallel and matching stops at the first hit in each one.
func searchTranscripts(sessions []*session, query string) map[string]bool {
	needle := []byte(strings.ToLower(query))
	hits := make([]bool, len(sessions))

	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i, s := range sessions {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			f, err := os.Open(path)
			if err != nil {
				return
			}
			defer f.Close()

			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
			for sc.Scan() {
				if bytes.Contains(bytes.ToLower(sc.Bytes()), needle) {
					hits[i] = true
					return
				}
			}
		}(i, s.File)
	}
	wg.Wait()

	out := map[string]bool{}
	for i, hit := range hits {
		if hit {
			out[sessions[i].ID] = true
		}
	}
	return out
}

// trashSessions reads the discarded transcripts as ordinary sessions, so the
// trash view can show the same rows as the timeline rather than a bare list of
// ids: you decide what to restore by reading titles, not UUIDs.
func trashSessions() []*session {
	var out []*session
	for _, t := range listTrash() {
		if s := scanFile(t.File); s != nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].End.After(out[j].End) })
	return out
}

// purge deletes a discarded session for good. It refuses anything outside the
// trash directory, so a stray call cannot reach a live transcript.
func purge(s *session) error {
	if !strings.HasPrefix(s.File, trashDir+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to delete outside the trash: %s", s.File)
	}
	os.RemoveAll(strings.TrimSuffix(s.File, ".jsonl"))
	return os.Remove(s.File)
}

// procParent returns a pid's parent, or 0 when the pid is gone.
func procParent(pid int) int {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	i := strings.LastIndex(string(stat), ")")
	if i < 0 {
		return 0
	}
	if fields := strings.Fields(string(stat)[i+1:]); len(fields) > 1 {
		ppid, _ := strconv.Atoi(fields[1])
		return ppid
	}
	return 0
}

// ancestorSession walks up from pid to the Claude Code process that owns it.
// `! cs pin` runs under a shell that claude spawned, possibly several levels
// down, so the direct parent is rarely the session process itself.
func ancestorSession(pid int, live map[string]liveRecord) string {
	byPid := map[int]string{}
	for id, rec := range live {
		if rec.Alive {
			byPid[rec.Pid] = id
		}
	}
	for ; pid > 1; pid = procParent(pid) {
		if id, ok := byPid[pid]; ok {
			return id
		}
	}
	return ""
}

// pinSelf bookmarks (or unbookmarks) the session cs was run from and returns
// what to call it.
func pinSelf(on bool) (string, error) {
	if !hasProc {
		return "", fmt.Errorf("finding the calling session needs /proc, which this system does not have")
	}
	id := ancestorSession(os.Getppid(), liveRecords())
	if id == "" {
		return "", fmt.Errorf("not inside a running Claude Code session: no parent process matches ~/.claude/sessions")
	}
	pins := loadPins()
	if on {
		pins[id] = true
	} else {
		delete(pins, id)
	}
	if err := savePins(pins); err != nil {
		return "", err
	}
	if files, _ := filepath.Glob(filepath.Join(projectsDir, "*", id+".jsonl")); len(files) > 0 {
		if s := scanFile(files[0]); s != nil && s.Title != "" {
			return s.Title, nil
		}
	}
	return id, nil
}

var inHerdr = os.Getenv("HERDR_ENV") == "1"

func tabLabel(s *session) string {
	if s.Title == "" {
		return s.ID[:min(8, len(s.ID))]
	}
	return trunc(s.Title, 32)
}

// spaceLabel names the herdr workspace a session opens in: one per project,
// matched by the directory's base name.
func spaceLabel(s *session) string { return filepath.Base(s.Cwd) }

// herdrArgs opens the session as a tab in its project's workspace, or creates
// that workspace when none is open. A new workspace's first tab takes the
// session, so no empty shell tab is left behind.
func herdrArgs(s *session, spaces map[string]string) []string {
	if ws, ok := spaces[spaceLabel(s)]; ok {
		return []string{"tab", "create", "--workspace", ws, "--cwd", s.Cwd, "--label", tabLabel(s), "--no-focus"}
	}
	return []string{"workspace", "create", "--cwd", s.Cwd, "--label", spaceLabel(s), "--no-focus"}
}

// herdrSpaces maps workspace label to id for every workspace open now.
func herdrSpaces() (map[string]string, error) {
	out, err := exec.Command("herdr", "workspace", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("herdr workspace list: %w", err)
	}
	return parseSpaces(out)
}

func parseSpaces(out []byte) (map[string]string, error) {
	var r struct {
		Result struct {
			Workspaces []struct {
				ID    string `json:"workspace_id"`
				Label string `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, err
	}
	spaces := map[string]string{}
	for _, w := range r.Result.Workspaces {
		spaces[w.Label] = w.ID
	}
	return spaces, nil
}

func ghosttyArgs(s *session) []string {
	return []string{"--working-directory=" + s.Cwd, "-e", "claude", "--resume", s.ID}
}

// created is what `herdr tab create` and `herdr workspace create` return;
// Workspace is only set by the latter.
type created struct {
	Workspace string
	Tab       string
	Pane      string
}

func parseCreated(out []byte) (created, error) {
	var r struct {
		Result struct {
			Workspace struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
			Tab struct {
				ID string `json:"tab_id"`
			} `json:"tab"`
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return created{}, err
	}
	c := created{r.Result.Workspace.ID, r.Result.Tab.ID, r.Result.RootPane.ID}
	if c.Pane == "" {
		return c, fmt.Errorf("herdr returned no pane id")
	}
	return c, nil
}

// openElsewhere resumes a session in herdr, in its project's workspace, or in
// a new ghostty window outside herdr, leaving this picker running. spaces is
// updated as workspaces are created so a project's later sessions join them.
func openElsewhere(s *session, spaces map[string]string) error {
	if inHerdr {
		args := herdrArgs(s, spaces)
		out, err := exec.Command("herdr", args...).Output()
		if err != nil {
			return fmt.Errorf("herdr %s %s: %w", args[0], args[1], err)
		}
		c, err := parseCreated(out)
		if err != nil {
			return err
		}
		if c.Workspace != "" {
			spaces[spaceLabel(s)] = c.Workspace
			exec.Command("herdr", "tab", "rename", c.Tab, tabLabel(s)).Run()
		}
		return exec.Command("herdr", "pane", "run", c.Pane, "claude --resume "+s.ID).Run()
	}
	c := exec.Command("ghostty", ghosttyArgs(s)...)
	c.Dir = s.Cwd
	// Its own session, so the window outlives cs and never reads our terminal.
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait()
	return nil
}
