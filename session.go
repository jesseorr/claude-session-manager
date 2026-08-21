package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type session struct {
	ID        string
	File      string
	Cwd       string
	Branch    string
	Title     string
	Last      string
	Turns     int
	Start     time.Time
	End       time.Time
	Pid       int
	Live      bool
	Bytes     int64  // transcript plus its subagent transcripts
	ProcStart string // recorded process start time; makes a recycled pid detectable
	Record    string // ~/.claude/sessions entry, if this session ever claimed one
	Status    string // what that process last said it was doing
}

var (
	home, _     = os.UserHomeDir()
	projectsDir = filepath.Join(home, ".claude", "projects")
	liveDir     = filepath.Join(home, ".claude", "sessions")
	historyFile = filepath.Join(home, ".claude", "history.jsonl")
)

// jsonStr pulls a string value out of a raw JSON line without parsing the whole
// object. Session transcripts hold megabytes of tool output we never look at.
func jsonStr(line, key string) string {
	k := `"` + key + `":"`
	i := strings.Index(line, k)
	if i < 0 {
		return ""
	}
	i += len(k) - 1
	j := i + 1
	for j < len(line) {
		if line[j] == '\\' {
			j += 2
			continue
		}
		if line[j] == '"' {
			break
		}
		j++
	}
	if j >= len(line) {
		return ""
	}
	v, err := strconv.Unquote(line[i : j+1])
	if err != nil {
		return ""
	}
	return v
}

func scanFile(path string) *session {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	s := &session{File: path, ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	if fi, err := f.Stat(); err == nil {
		s.End, s.Bytes = fi.ModTime(), fi.Size()
	}
	// Subagent transcripts sit in a directory named for the session and can
	// dwarf it, so they count toward what discarding this session reclaims.
	if subs, err := os.ReadDir(strings.TrimSuffix(path, ".jsonl")); err == nil {
		for _, e := range subs {
			if fi, err := e.Info(); err == nil {
				s.Bytes += fi.Size()
			}
		}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if s.Start.IsZero() && strings.Contains(line, `"timestamp":"`) {
			if t, err := time.Parse(time.RFC3339, jsonStr(line, "timestamp")); err == nil {
				s.Start = t.Local()
			}
		}
		if s.Cwd == "" && strings.Contains(line, `"cwd":"`) {
			s.Cwd = jsonStr(line, "cwd")
			s.Branch = jsonStr(line, "gitBranch")
		}
		if strings.Contains(line, `"aiTitle":"`) {
			s.Title = jsonStr(line, "aiTitle")
		}
		if strings.Contains(line, `"type":"last-prompt"`) {
			s.Turns++
			s.Last = jsonStr(line, "lastPrompt")
		}
	}
	if s.Start.IsZero() {
		s.Start = s.End
	}
	if s.Cwd == "" {
		s.Cwd = decodePath(filepath.Base(filepath.Dir(path)))
	}
	return s
}

// decodePath undoes Claude Code's project directory encoding, which replaces
// every "/" with "-" and so cannot be reversed on its own: "one-offs" and
// "one/offs" encode identically. Each segment is therefore extended with the
// next token until the resulting path exists on disk.
func decodePath(enc string) string {
	parts := strings.Split(strings.TrimPrefix(enc, "-"), "-")
	cur := "/"
	for i := 0; i < len(parts); {
		seg, next := parts[i], i+1
		for j := i + 1; j <= len(parts); j++ {
			if _, err := os.Stat(filepath.Join(cur, seg)); err == nil {
				next = j
				break
			}
			if j == len(parts) {
				break
			}
			seg = seg + "-" + parts[j]
		}
		if next == i+1 {
			seg = parts[i] // nothing on disk matched; keep the shortest guess
		}
		cur, i = filepath.Join(cur, seg), next
	}
	return cur
}

func collect() []*session {
	entries, _ := os.ReadDir(projectsDir)
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(projectsDir, e.Name()))
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".jsonl") {
				paths = append(paths, filepath.Join(projectsDir, e.Name(), f.Name()))
			}
		}
	}

	out := make([]*session, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i, p := range paths {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			out[i] = scanFile(p)
			<-sem
		}(i, p)
	}
	wg.Wait()

	live := liveRecords()
	res := out[:0]
	for _, s := range out {
		if s == nil {
			continue
		}
		if rec, ok := live[s.ID]; ok {
			s.Record, s.Status, s.ProcStart = rec.File, rec.Status, rec.ProcStart
			if rec.Alive {
				s.Pid, s.Live = rec.Pid, true
			}
		}
		res = append(res, s)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].End.After(res[j].End) })
	return res
}

// liveRecord is one entry from ~/.claude/sessions: a session process that
// claimed a pid, plus the status label that process last wrote about itself.
type liveRecord struct {
	Pid       int
	File      string
	Status    string
	ProcStart string
	Alive     bool
}

// hasProc reports whether this system exposes /proc, which is where every
// liveness answer comes from. On a system without it the answer is "unknown",
// and unknown must never be treated as "dead": that would let the sweep delete
// the records of sessions that are still running.
var hasProc = func() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
}()

// liveRecords reads every session record and checks each against /proc. The
// pid alone is not enough — pids get reused — so the recorded process start
// time must match too. Records whose process is gone come back Alive: false
// rather than being dropped, because those are exactly the stale entries that
// make a session look like it is still running when it is not.
func liveRecords() map[string]liveRecord {
	out := map[string]liveRecord{}
	entries, _ := os.ReadDir(liveDir)
	for _, e := range entries {
		path := filepath.Join(liveDir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var d struct {
			Pid       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			ProcStart string `json:"procStart"`
			Status    string `json:"status"`
		}
		if json.Unmarshal(b, &d) != nil || d.SessionID == "" {
			continue
		}

		rec := liveRecord{Pid: d.Pid, File: path, Status: d.Status, ProcStart: d.ProcStart}
		// Every clause matters: procStart returns "" both for a pid that does
		// not exist and on a system with no /proc, and a record missing its
		// own procStart would otherwise compare equal to that "" and be called
		// alive — handing a bogus pid to the kill path.
		if start := procStart(d.Pid); hasProc && d.Pid > 0 && start != "" && d.ProcStart != "" {
			rec.Alive = start == d.ProcStart
		}
		// Keep whichever record is actually running, if two claim one session.
		if old, seen := out[d.SessionID]; !seen || rec.Alive || !old.Alive {
			out[d.SessionID] = rec
		}
	}
	return out
}

// procStart returns a pid's start time, the field that makes a pid unique over
// the life of a boot. An empty result means no such process.
func procStart(pid int) string {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	i := strings.LastIndex(string(stat), ")")
	if i < 0 {
		return ""
	}
	if fields := strings.Fields(string(stat)[i+1:]); len(fields) > 19 {
		return fields[19]
	}
	return ""
}

type prompt struct {
	At   time.Time
	Text string
}

func promptsFor(id string) []prompt {
	f, err := os.Open(historyFile)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []prompt
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, id) {
			continue
		}
		var d struct {
			Display   string `json:"display"`
			Timestamp int64  `json:"timestamp"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal([]byte(line), &d) != nil || d.SessionID != id {
			continue
		}
		out = append(out, prompt{time.UnixMilli(d.Timestamp), d.Display})
	}
	return out
}

// rename appends the same ai-title record Claude Code writes itself, so the new
// name shows up in `claude --resume` too. The file's mtime is restored so a
// rename does not push the session to the top of the list.
func rename(s *session, title string) error {
	fi, err := os.Stat(s.File)
	if err != nil {
		return err
	}
	rec, _ := json.Marshal(map[string]string{"type": "ai-title", "aiTitle": title, "sessionId": s.ID})

	f, err := os.OpenFile(s.File, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(rec, '\n')); err != nil {
		f.Close()
		return err
	}
	f.Close()

	s.Title = title
	return os.Chtimes(s.File, fi.ModTime(), fi.ModTime())
}

func tilde(p string) string {
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func dayLabel(t time.Time) string {
	y, m, d := t.Date()
	if ty, tm, td := time.Now().Date(); y == ty && m == tm && d == td {
		return "Today"
	}
	if yy, ym, yd := time.Now().AddDate(0, 0, -1).Date(); y == yy && m == ym && d == yd {
		return "Yesterday"
	}
	return t.Format("Mon Jan 02")
}
