package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	stDim    = lipgloss.NewStyle().Faint(true)
	stBold   = lipgloss.NewStyle().Bold(true)
	stCyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	stYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	stGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	stSel    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	stPlus   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	stMinus  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	selBg    = lipgloss.Color("238")

	// Bars are filled to the full terminal width, so their text has to stay
	// unstyled: an inner colour reset would punch a hole in the background.
	stBar     = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("250"))
	stBarKeys = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("245"))
	stBarIn   = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("221"))
	stBarWarn = lipgloss.NewStyle().Background(lipgloss.Color("124")).Foreground(lipgloss.Color("231")).Bold(true)
	stBarErr  = lipgloss.NewStyle().Background(lipgloss.Color("52")).Foreground(lipgloss.Color("231"))
	// The two side views get their own header colour, so which list you are
	// looking at is answered before reading a word of it.
	stBarPins  = lipgloss.NewStyle().Background(lipgloss.Color("54")).Foreground(lipgloss.Color("231"))
	stBarTrash = lipgloss.NewStyle().Background(lipgloss.Color("58")).Foreground(lipgloss.Color("231"))
)

// pal is the set of styles one row is drawn with. The highlighted row swaps in
// brighter foregrounds over a filled background; because a terminal colour
// reset ends the background too, every segment of that row — separators, gaps
// and indents included — has to be rendered through one of these styles.
type pal struct {
	mark, check, num, title, path, dim, time, size, live, plus, minus, fill lipgloss.Style
	selected                                                                bool
}

// pad fills the rest of the row so the highlight reaches the screen edge.
func (p pal) pad(styled, plain string, width int) string {
	if !p.selected {
		return styled
	}
	if n := width - len([]rune(plain)); n > 0 {
		return styled + p.fill.Render(strings.Repeat(" ", n))
	}
	return styled
}

func rowStyles(selected bool) pal {
	if !selected {
		return pal{mark: stSel, check: stGreen, num: stYellow, title: stBold, path: stCyan,
			dim: stDim, time: lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
			size: lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
			live: stGreen, plus: stPlus, minus: stMinus, fill: lipgloss.NewStyle()}
	}
	on := func(color string) lipgloss.Style {
		return lipgloss.NewStyle().Background(selBg).Foreground(lipgloss.Color(color))
	}
	return pal{
		mark:  on("13").Bold(true),
		check: on("10"),
		num:   on("11"),
		title: on("231").Bold(true),
		path:  on("14"),
		dim:   on("250"),
		time:  on("255"),
		size:  on("252"),
		live:  on("10"),
		plus:  on("10"),
		minus: on("9"),
		fill:  lipgloss.NewStyle().Background(selBg),

		selected: true,
	}
}

// barSplit paints a full-width strip with text pushed to both edges.
func barSplit(style lipgloss.Style, width int, left, right string) string {
	if width <= 0 {
		width = 80
	}
	gap := width - len([]rune(left)) - len([]rune(right)) - 2
	if gap < 1 {
		return bar(style, width, left)
	}
	return style.Inline(true).Width(width).Render(" " + left + strings.Repeat(" ", gap) + right + " ")
}

// scrollBar draws where the viewport sits in the whole list. It returns empty
// when everything already fits, since a full-length thumb tells you nothing.
func scrollBar(offset, visible, total, track int) string {
	maxOffset := total - visible
	if maxOffset <= 0 || track < 4 {
		return ""
	}
	// The thumb travels 0..track-thumb as the offset travels 0..maxOffset, so
	// the last row puts it flush against the end instead of a rounding error
	// short of it, which reads as "there is still more below".
	thumb := max(1, visible*track/total)
	start := offset * (track - thumb) / maxOffset
	return strings.Repeat("─", start) + strings.Repeat("█", thumb) +
		strings.Repeat("─", track-start-thumb)
}

// bar paints one full-width strip of chrome at the top or bottom of the screen.
func bar(style lipgloss.Style, width int, text string) string {
	if width <= 0 {
		width = 80
	}
	return style.Inline(true).Width(width).Render(" " + trunc(text, width-2))
}

const pruneMinTurns = 3

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func usage() {
	fmt.Print(`cs - Claude Code session index

  cs [query]        interactive picker (arrows/jk, enter resumes)
  cs -l [query]     plain listing, no TUI
  cs --prune        open with throwaway sessions pre-marked, review and press t
  cs --gc           drop session records whose process is gone
  cs --trash        list discarded sessions
  cs --restore ID   put a discarded session back

  -v     print the version
  -d N   only sessions active in the last N days (default 7)
  -a     every session ever
  -p     only sessions started in the current directory

Shift opens a view, the plain letter is the verb that fills it: T is the trash
and t sends a session there, P is bookmarks and p adds one. Inside a view the
same letter takes things back out. Press ? inside for the rest.

  enter  resume         space  mark          t  trash it   ·  T  the trash
  n      new session    A      mark all      p  bookmark   ·  P  bookmarks
  f      resume a fork  ctrl+a mark junk     c  close (SIGTERM, history kept)
  v      prompts        esc    unwind        D  delete for good, in the trash
  R      rename         r      rescan        ctrl+t  empty the trash
  /      filter         ctrl+/ search        w  window · s sort · h here

t, p, c and D act on every marked session, or on the highlighted one when
nothing is marked. Trashed sessions move to ~/.claude/.cs-trash.
Scope, sort and directory settings persist in ~/.claude/cs-prefs.json.
`)
}

func main() {
	saved := loadPrefs()
	days, here := saved.Days, saved.Here
	all, plain, prune := false, false, false
	var query string

	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d", "--days":
			i++
			if i < len(args) {
				days, _ = strconv.Atoi(args[i])
			}
		case "-a", "--all":
			all = true
		case "-p", "--here":
			here = true
		case "-l", "--list":
			plain = true
		case "--gc":
			if !hasProc {
				fmt.Fprintln(os.Stderr, "--gc needs /proc, which this system does not have")
				os.Exit(1)
			}
			fmt.Printf("cleared %d stale session records\n", gcRecords())
			return
		case "--prune":
			prune, all = true, true
		case "--trash":
			for _, t := range listTrash() {
				fmt.Printf("%s  %s\n", t.ID, stDim.Render(decodePath(t.Project)))
			}
			return
		case "--restore":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--restore needs a session id")
				os.Exit(1)
			}
			if err := restore(args[i]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "-v", "--version":
			fmt.Printf("cs %s\n", version)
			return
		case "-h", "--help":
			usage()
			return
		default:
			query = args[i]
		}
	}
	pins := loadPins()
	cwd, _ := os.Getwd()
	if all {
		days = 0
	}

	m := model{all: collect(), query: query, pins: pins, days: days, hereOnly: here,
		sortBy: saved.Sort, cwd: cwd, w: 100, h: 30,
		marked: map[string]bool{}, files: map[string][]fileEdit{}}
	if m.sortBy < 0 || m.sortBy >= len(sortNames) {
		m.sortBy = sortRecent
	}

	if plain {
		m.shown, m.cursor = match(m.inScope(), query), -1
		m.sortShown()
		lines, _, _ := m.renderList()
		for _, l := range lines {
			fmt.Println(l)
		}
		return
	}

	m.apply()
	if prune {
		for _, s := range pruneCandidates(m.shown, pins, pruneMinTurns) {
			m.marked[s.ID] = true
		}
	}
	m.loadFiles()

	fm, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if dir := fm.(model).newDir; dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		argv := []string{"claude"}
		if p := strings.TrimSpace(fm.(model).newText); p != "" {
			argv = append(argv, p)
		}
		bin, err := exec.LookPath("claude")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		syscall.Exec(bin, argv, os.Environ())
	}
	if t := fm.(model).resume; t != nil {
		if err := os.Chdir(t.Cwd); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		bin, err := exec.LookPath("claude")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		argv := []string{"claude", "--resume", t.ID}
		if fm.(model).fork {
			argv = append(argv, "--fork-session")
		}
		syscall.Exec(bin, argv, os.Environ())
	}
}

func match(in []*session, q string) []*session {
	if q == "" {
		return in
	}
	q = strings.ToLower(q)
	var out []*session
	for _, s := range in {
		hay := strings.ToLower(s.Title + " " + s.Cwd + " " + s.Last + " " + s.Branch)
		if strings.Contains(hay, q) {
			out = append(out, s)
		}
	}
	return out
}

func trunc(s string, n int) string {
	if n <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// render lays every session out as a day header plus a three-line block, and
// reports the first line index of each session. Passing cursor < 0 renders the
// plain, non-interactive listing.
//
// Each block reads title first, since that is what the eye scans for:
//
//	✓  3  Trace the retry loop in the upload worker
//	      ~/work/api-gateway · main · first … last … · 144 turns · ●live
//	      ↳ yes, proceed
//
// renderList also reports, for every line, the index of the group heading that
// line falls under, so a heading scrolled off the top can be pinned back on.
func (m model) renderList() ([]string, []int, []int) {
	sessions, cursor, width := m.shown, m.cursor, m.w
	pins, marked := m.pins, m.marked
	var cursorFiles []fileEdit
	if s := m.sel(); s != nil {
		cursorFiles = m.files[s.ID]
	}
	if width <= 0 {
		width = 200
	}
	const indent = "        "
	body := width - len(indent) - 1

	// When and how big are flushed to the right edge so they hold one column on
	// every row. Turn counts are padded to a common width and the live column
	// is reserved on dead rows, so nothing shifts as the eye travels down.
	// Styled text carries escape codes, so all measuring happens on plain
	// strings and every line is assembled as a (styled, plain) pair.
	lefts := make([]string, len(sessions))
	digits, anyLive := 1, false
	for i, s := range sessions {
		lefts[i] = tilde(s.Cwd)
		if s.Branch != "" && s.Branch != "HEAD" {
			lefts[i] += " · " + s.Branch
		}
		digits = max(digits, len(strconv.Itoa(s.Turns)))
		anyLive = anyLive || s.Live
	}
	rightW := len(stamp("first", time.Time{})) + 3 + len(stamp("last ", time.Time{})) +
		3 + digits + len(" turns")
	if anyLive {
		rightW += 3 + len("●live")
	}

	var lines []string
	var under []int // group heading index governing each line
	at := make([]int, len(sessions))
	group, groupAt := "", 0
	push := func(l ...string) {
		for _, one := range l {
			lines, under = append(lines, one), append(under, groupAt)
		}
	}
	for i, s := range sessions {
		g := dayLabel(s.End)
		if m.sortBy == sortProject {
			g = tilde(s.Cwd)
		}
		if g != group {
			group = g
			if len(lines) > 0 {
				push("")
			}
			head := stBold.Render(g)
			if m.sortBy != sortProject {
				head += "  " + stDim.Render(s.End.Format("2006-01-02"))
			}
			groupAt = len(lines)
			push(head, "")
		} else if i > 0 {
			push("")
		}

		p := rowStyles(i == cursor)

		cur, mark := "  ", "  "
		if i == cursor {
			cur = "▸ "
		}
		if marked[s.ID] {
			mark = "✓ "
		}
		star := ""
		if pins[s.ID] {
			star = "★ "
		}
		titleText := trunc(star+s.Title, body)
		titleStyle := p.title
		if s.Title == "" {
			titleText, titleStyle = star+"(untitled)", p.dim
		}
		head := p.mark.Render(cur) + p.check.Render(mark) +
			p.num.Render(fmt.Sprintf("%2d  ", i+1)) + titleStyle.Render(titleText)
		headPlain := cur + mark + fmt.Sprintf("%2d  ", i+1) + titleText

		leftPlain := trunc(lefts[i], max(12, body-rightW-2))
		left := p.path.Render(leftPlain)
		if dir, branch, ok := strings.Cut(leftPlain, " · "); ok {
			left = p.path.Render(dir) + p.dim.Render(" · "+branch)
		}
		first, last := stamp("first", s.Start), stamp("last ", s.End)
		turns := fmt.Sprintf("%*d turns", digits, s.Turns)
		metaPlain := first + " · " + last + " · " + turns
		meta := p.dim.Render("first ") + p.time.Render(first[6:]) +
			p.dim.Render(" · last  ") + p.time.Render(last[6:]) +
			p.dim.Render(" · "+turns)
		if s.Live {
			meta, metaPlain = meta+p.dim.Render(" · ")+p.live.Render("●live"), metaPlain+" · ●live"
		} else if anyLive {
			pad := strings.Repeat(" ", 3+len("●live"))
			meta, metaPlain = meta+p.fill.Render(pad), metaPlain+pad
		}
		gap := max(2, body-len([]rune(leftPlain))-len([]rune(metaPlain)))
		metaLine := p.fill.Render(indent) + left + p.fill.Render(strings.Repeat(" ", gap)) + meta
		metaPlainLine := indent + leftPlain + strings.Repeat(" ", gap) + metaPlain

		lastText := trunc("↳ "+strings.Join(strings.Fields(s.Last), " "), body)

		at[i] = len(lines)
		push(
			p.pad(head, headPlain, width),
			p.pad(metaLine, metaPlainLine, width),
			p.pad(p.fill.Render(indent)+p.dim.Render(lastText), indent+lastText, width))
		if i == cursor {
			styled, plain := hoverLine(s, cursorFiles, body, p)
			push(p.pad(p.fill.Render(indent)+styled, indent+plain, width))
		}
	}
	return lines, at, under
}

// hoverLine is the extra line the highlighted row gets: what this session
// costs on disk, then the files it changed, biggest edit first.
func hoverLine(sess *session, edits []fileEdit, width int, p pal) (string, string) {
	const show = 4
	size := humanSize(sess.Bytes)
	parts, plainParts := []string{p.size.Render(size)}, []string{size}

	if len(edits) == 0 {
		parts, plainParts = append(parts, p.dim.Render("no files changed")), append(plainParts, "no files changed")
	}
	for i, e := range edits {
		if i == show {
			more := fmt.Sprintf("+%d more", len(edits)-show)
			parts, plainParts = append(parts, p.dim.Render(more)), append(plainParts, more)
			break
		}
		name := filepath.Base(e.Path)
		if rel, err := filepath.Rel(sess.Cwd, e.Path); err == nil && !strings.HasPrefix(rel, "..") {
			name = rel
		}
		if i == 0 {
			name = "\u270e " + name
		}
		plus, minus := fmt.Sprintf(" +%d", e.Plus), ""
		styled := p.plus.Render(plus)
		if e.Minus > 0 {
			minus = fmt.Sprintf("/-%d", e.Minus)
			styled += p.dim.Render("/") + p.minus.Render(minus[1:])
		}
		parts = append(parts, p.dim.Render(name)+styled)
		plainParts = append(plainParts, name+plus+minus)
	}

	plain := strings.Join(plainParts, " \u00b7 ")
	if len([]rune(plain)) > width {
		plain = trunc(plain, width)
		return p.dim.Render(plain), plain
	}
	return strings.Join(parts, p.dim.Render(" \u00b7 ")), plain
}

const (
	modeList = iota
	modeDetail
	modeRename
	modeFilter
	modeConfirm
	modeHelp
	modeSearch
	modeLive
	modeNew
)

// Sort orders the s key cycles through. Sorting by project also switches the
// group headings from days to directories.
const (
	sortRecent = iota
	sortSize
	sortTurns
	sortProject
)

var sortNames = []string{"recent", "size", "turns", "project"}

// timeframes is the cycle the w key walks; 0 means every session ever.
var timeframes = []int{1, 7, 30, 90, 0}

const (
	actClose = iota
	actDiscard
	actPurge
	actEmpty
)

type model struct {
	all        []*session
	shown      []*session
	days       int  // 0 means no time limit
	hereOnly   bool // only sessions started in cwd
	cwd        string
	trashView  bool // showing discarded sessions instead of live ones
	trashAll   []*session
	notice     string
	sortBy     int
	pinsOnly   bool            // showing bookmarks instead of the timeline
	hits       map[string]bool // transcript search results; nil means no search
	hitQuery   string
	prevQuery  string // what / reverts to when cancelled
	marked     map[string]bool
	pins       map[string]bool
	files      map[string][]fileEdit
	cursor     int
	offset     int
	w, h       int
	mode       int
	action     int
	query      string
	input      string
	prompts    []prompt
	detailAt   int
	resume     *session
	fork       bool
	newStep    int
	newPick    int
	newText    string
	newDir     string // set when the user commits to starting a session here
	newSuggest []dirSuggest
	err        string
}

// inScope applies the time window and directory filter that the w and h keys
// drive. Pinned sessions ignore the window, since pinning says "keep this in
// front of me" and a cutoff silently dropping it would defeat that.
func (m model) inScope() []*session {
	var out []*session
	if m.trashView {
		return m.trashAll
	}
	if m.pinsOnly {
		for _, s := range m.all {
			if m.pins[s.ID] {
				out = append(out, s)
			}
		}
		return out
	}

	cutoff := time.Time{}
	if m.days > 0 {
		cutoff = time.Now().AddDate(0, 0, -m.days)
	}
	for _, s := range m.all {
		if s.End.Before(cutoff) || (m.days > 0 && s.Turns == 0) {
			continue
		}
		if m.hereOnly && s.Cwd != m.cwd {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (m model) scopeLabel() string {
	switch {
	case m.days == 1:
		return "today"
	case m.days > 0:
		return fmt.Sprintf("%dd", m.days)
	}
	return "all"
}

// sortShown orders the visible list, pinned rows first so a pin always wins
// over whatever the s key is currently sorting by.
func (m *model) sortShown() {
	switch m.sortBy {
	case sortSize:
		sort.SliceStable(m.shown, func(i, j int) bool { return m.shown[i].Bytes > m.shown[j].Bytes })
	case sortTurns:
		sort.SliceStable(m.shown, func(i, j int) bool { return m.shown[i].Turns > m.shown[j].Turns })
	case sortProject:
		sort.SliceStable(m.shown, func(i, j int) bool {
			if a, b := m.shown[i].Cwd, m.shown[j].Cwd; a != b {
				return a < b
			}
			return m.shown[i].End.After(m.shown[j].End)
		})
	default:
		sort.SliceStable(m.shown, func(i, j int) bool { return m.shown[i].End.After(m.shown[j].End) })
	}
}

func (m *model) apply() {
	m.shown = match(m.inScope(), m.query)
	if m.hits != nil {
		var keep []*session
		for _, s := range m.shown {
			if m.hits[s.ID] {
				keep = append(keep, s)
			}
		}
		m.shown = keep
	}
	m.sortShown()
	if m.cursor >= len(m.shown) {
		m.cursor = len(m.shown) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// targets is what t, p, c and D act on: every marked session, or the
// highlighted one when nothing is marked. Trashed sessions live in their own
// slice, so marks made in the trash resolve against that one.
func (m model) targets() []*session {
	if len(m.marked) == 0 {
		if s := m.sel(); s != nil {
			return []*session{s}
		}
		return nil
	}
	src := m.all
	if m.trashView {
		src = m.trashAll
	}
	var out []*session
	for _, s := range src {
		if m.marked[s.ID] {
			out = append(out, s)
		}
	}
	return out
}

// scroll keeps the highlighted session's block inside the viewport. It runs on
// every key so the offset survives into the next View.
func (m *model) scroll() {
	body := m.h - 2
	if body < 4 || len(m.shown) == 0 {
		return
	}
	lines, at, under := m.renderList()
	top := at[m.cursor]
	if top-1 < m.offset {
		m.offset = max(0, top-1)
	}
	if top+4 > m.offset+body {
		m.offset = top + 4 - body
	}
	m.offset = min(m.offset, max(0, len(lines)-body))
	// If the group heading sits just above the viewport, show it rather than
	// pinning a copy of it: scrolling to the first row of a group otherwise
	// hides the real heading one line up and fakes it back as sticky.
	if m.offset < len(under) && m.offset-under[m.offset] <= 2 {
		m.offset = under[m.offset]
	}
}

// loadFiles computes the changed-file summary for the highlighted session the
// first time it is highlighted; the diffs are too big to parse for every row.
func (m *model) loadFiles() {
	s := m.sel()
	if s == nil {
		return
	}
	if _, done := m.files[s.ID]; !done {
		m.files[s.ID] = fileStats(s.File)
	}
}

type tickMsg struct{}
type searchMsg struct {
	query string
	hits  map[string]bool
}

// tick re-reads only which sessions are alive. That is a handful of small
// files, cheap enough to run while the list is open; r does the full re-scan.
func tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m model) savePrefs() {
	savePrefs(prefs{Days: m.days, Here: m.hereOnly, Sort: m.sortBy})
}

func (m *model) refreshLive() {
	live := liveRecords()
	for _, s := range m.all {
		s.Pid, s.Live = 0, false
		if rec, ok := live[s.ID]; ok {
			s.Record, s.Status = rec.File, rec.Status
			if rec.Alive {
				s.Pid, s.Live = rec.Pid, true
			}
		}
	}
}

func (m model) Init() tea.Cmd { return tick() }

func (m model) sel() *session {
	if m.cursor >= 0 && m.cursor < len(m.shown) {
		return m.shown[m.cursor]
	}
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.refreshLive()
		return m, tick()
	case searchMsg:
		m.hits, m.hitQuery, m.cursor, m.offset = msg.hits, msg.query, 0, 0
		m.notice = fmt.Sprintf("%d sessions mention %q", len(msg.hits), msg.query)
		if len(msg.hits) == 0 {
			m.notice = fmt.Sprintf("nothing mentions %q — esc clears the search", msg.query)
		}
		m.apply()
		m.loadFiles()
		return m, nil
	case tea.KeyMsg:
		switch m.mode {
		case modeRename:
			return m.updateRename(msg)
		case modeFilter:
			return m.updateFilter(msg)
		case modeDetail:
			return m.updateDetail(msg)
		case modeConfirm:
			return m.updateConfirm(msg)
		case modeHelp:
			switch msg.String() {
			case "up", "k":
				m.detailAt = max(0, m.detailAt-1)
			case "down", "j":
				m.detailAt++
			case "pgup", "ctrl+u":
				m.detailAt = max(0, m.detailAt-10)
			case "pgdown", "ctrl+d":
				m.detailAt += 10
			case "g":
				m.detailAt = 0
			default:
				m.mode = modeList
			}
			return m, nil
		case modeSearch:
			return m.updateSearch(msg)
		case modeLive:
			return m.updateLive(msg)
		case modeNew:
			return m.updateNew(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Keys pressed faster than one read arrive batched ("jj" as one message).
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		var cmd tea.Cmd
		var mm tea.Model = m
		for _, r := range msg.Runes {
			var c tea.Cmd
			mm, c = mm.(model).updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			if c != nil {
				cmd = c
			}
		}
		return mm, cmd
	}
	m.err, m.notice = "", ""
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		switch {
		case len(m.marked) > 0:
			m.marked = map[string]bool{}
		case m.hits != nil:
			m.hits, m.hitQuery, m.cursor = nil, "", 0
			m.apply()
		case m.query != "":
			m.query, m.cursor = "", 0
			m.apply()
		case m.trashView || m.pinsOnly:
			m.trashView, m.pinsOnly, m.trashAll = false, false, nil
			m.cursor, m.offset = 0, 0
			m.apply()
			m.loadFiles()
		default:
			return m, tea.Quit
		}
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.shown)-1 {
			m.cursor++
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = len(m.shown) - 1
	case "pgup", "ctrl+u":
		m.cursor = max(0, m.cursor-8)
	case "pgdown", "ctrl+d":
		m.cursor = min(len(m.shown)-1, m.cursor+8)
	case " ":
		if s := m.sel(); s != nil {
			if m.marked[s.ID] {
				delete(m.marked, s.ID)
			} else {
				m.marked[s.ID] = true
			}
			m.cursor = min(len(m.shown)-1, m.cursor+1)
		}
	case "A":
		allMarked := len(m.shown) > 0
		for _, s := range m.shown {
			if !m.marked[s.ID] {
				allMarked = false
				break
			}
		}
		for _, s := range m.shown {
			if allMarked {
				delete(m.marked, s.ID)
			} else {
				m.marked[s.ID] = true
			}
		}
	case "enter":
		// Nothing to open in the trash: everywhere else enter loads the chat,
		// and a discarded transcript is not one you can resume. t restores.
		if m.trashView {
			break
		}
		if s := m.sel(); s != nil {
			if s.Live {
				m.mode = modeLive
				return m, nil
			}
			m.resume = s
			return m, tea.Quit
		}
	case "f":
		if s := m.sel(); s != nil {
			m.resume, m.fork = s, true
			return m, tea.Quit
		}
	case "v":
		if s := m.sel(); s != nil {
			m.prompts, m.detailAt, m.mode = promptsFor(s.ID), 0, modeDetail
		}
	case "R":
		if s := m.sel(); s != nil {
			m.input, m.mode = s.Title, modeRename
		}
	case "r":
		m.all = collect()
		m.apply()
		m.loadFiles()
		m.notice = "rescanned " + fmt.Sprint(len(m.all)) + " sessions"
	case "s":
		m.sortBy = (m.sortBy + 1) % len(sortNames)
		m.cursor, m.offset = 0, 0
		m.apply()
		m.savePrefs()
		m.notice = "sorted by " + sortNames[m.sortBy]
	case "ctrl+_", "ctrl+/":
		m.input, m.mode = "", modeSearch
	case "p":
		if m.trashView {
			m.notice = "restore it first, then bookmark it"
			break
		}
		for _, s := range m.targets() {
			if m.pins[s.ID] {
				delete(m.pins, s.ID)
			} else {
				m.pins[s.ID] = true
			}
		}
		if err := savePins(m.pins); err != nil {
			m.err = err.Error()
		}
		m.marked = map[string]bool{}
		m.apply()
		m.cursor = min(m.cursor, max(0, len(m.shown)-1))
	case "c":
		if m.trashView {
			m.notice = "these are already closed — t restores, D deletes for good"
			break
		}
		if len(m.targets()) > 0 {
			m.action, m.mode = actClose, modeConfirm
		}
	case "t":
		if m.trashView {
			m.restoreTargets()
			break
		}
		if len(m.targets()) > 0 {
			m.action, m.mode = actDiscard, modeConfirm
		}
	case "ctrl+t":
		if m.trashView && len(m.trashAll) > 0 {
			m.action, m.mode = actEmpty, modeConfirm
		}
	case "D":
		if m.trashView && len(m.targets()) > 0 {
			m.action, m.mode = actPurge, modeConfirm
		}
	case "/":
		m.input, m.prevQuery, m.mode = m.query, m.query, modeFilter
	case "w":
		next := 0
		for i, d := range timeframes {
			if d == m.days {
				next = (i + 1) % len(timeframes)
			}
		}
		m.days = timeframes[next]
		m.savePrefs()
		m.notice = "showing " + m.scopeLabel()
		m.cursor, m.offset = 0, 0
		m.apply()
	case "h":
		m.hereOnly = !m.hereOnly
		m.savePrefs()
		m.notice = "all directories"
		if m.hereOnly {
			m.notice = "only " + tilde(m.cwd)
		}
		m.cursor, m.offset = 0, 0
		m.apply()
	case "P":
		m.pinsOnly, m.trashView = !m.pinsOnly, false
		m.cursor, m.offset = 0, 0
		m.apply()

	case "ctrl+a":
		n := 0
		for _, s := range pruneCandidates(m.shown, m.pins, pruneMinTurns) {
			m.marked[s.ID], n = true, n+1
		}
		m.notice = fmt.Sprintf("marked %d throwaway sessions — t sends them to the trash", n)
		if n == 0 {
			m.notice = "nothing to prune in this scope"
		}
	case "T":
		m.trashView, m.pinsOnly = !m.trashView, false
		m.trashAll = nil
		if m.trashView {
			m.trashAll = trashSessions()
		}
		m.cursor, m.offset, m.marked = 0, 0, map[string]bool{}
		m.apply()
		m.loadFiles()
		// The footer already names the keys for the view being entered, so a
		// notice here would only cover it up.
	case "ctrl+g":
		m.notice = fmt.Sprintf("cleared %d stale session records", gcRecords())
		if !hasProc {
			m.notice = "this needs /proc, which this system does not have"
		}
	case "n":
		m.mode, m.newStep, m.newPick = modeNew, newWhere, -1
		m.newSuggest, m.newText = suggestDirs(m.all), ""
		m.input = tilde(m.cwd)
	case "?":
		m.mode, m.detailAt = modeHelp, 0
	}
	m.loadFiles()
	m.scroll()
	return m, nil
}

func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		gone := map[string]bool{}
		var failed []string
		victims := m.targets()
		if m.action == actEmpty {
			victims = m.trashAll
		}
		for _, s := range victims {
			var err error
			switch m.action {
			case actClose:
				err = closeSession(s)
			case actPurge, actEmpty:
				if err = purge(s); err == nil {
					gone[s.ID] = true
				}
			default:
				if err = discard(s); err == nil {
					gone[s.ID] = true
				}
			}
			if err != nil {
				failed = append(failed, err.Error())
			}
		}
		if len(gone) > 0 {
			drop := func(in []*session) []*session {
				var keep []*session
				for _, s := range in {
					if !gone[s.ID] {
						keep = append(keep, s)
					}
				}
				return keep
			}
			m.all, m.trashAll = drop(m.all), drop(m.trashAll)
			if m.action == actDiscard {
				m.trashAll = nil // rescanned next time the trash is opened
			}
		}
		if len(failed) > 0 {
			m.err = fmt.Sprintf("%d failed: %s", len(failed), failed[0])
		}
		m.marked = map[string]bool{}
		m.mode = modeList
		m.apply()
		m.cursor = min(m.cursor, max(0, len(m.shown)-1))
		m.scroll()
	case "n", "N", "esc", "q", "ctrl+c":
		m.mode = modeList
	}
	return m, nil
}

func (m model) updateRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeList
	case "enter":
		if s := m.sel(); s != nil && strings.TrimSpace(m.input) != "" {
			if err := rename(s, strings.TrimSpace(m.input)); err != nil {
				m.err = err.Error()
			}
		}
		m.mode = modeList
	case "backspace":
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.input += msg.String()
		}
	}
	return m, nil
}

func (m model) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.query, m.mode = m.prevQuery, modeList
		m.cursor = 0
		m.apply()
	case "enter":
		m.query, m.mode = m.input, modeList
		m.apply()
	case "backspace":
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
		m.query = m.input
		m.apply()
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.input += msg.String()
			m.query = m.input
			m.apply()
		}
	}
	m.loadFiles()
	m.scroll()
	return m, nil
}

func (m model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "v":
		m.mode = modeList
	case "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.detailAt = max(0, m.detailAt-1)
	case "down", "j":
		m.detailAt++
	case "pgup", "ctrl+u":
		m.detailAt = max(0, m.detailAt-10)
	case "pgdown", "ctrl+d":
		m.detailAt += 10
	case "g":
		m.detailAt = 0
	case "enter":
		if s := m.sel(); s != nil {
			m.resume = s
			return m, tea.Quit
		}
	}
	return m, nil
}

// updateLive handles enter on a session that is still running. Resuming it
// would leave two processes appending to one transcript, so the choice is
// spelled out rather than assumed.
func (m model) updateLive(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.sel()
	switch msg.String() {
	case "f":
		m.resume, m.fork, m.mode = s, true, modeList
		return m, tea.Quit
	case "r":
		m.resume, m.mode = s, modeList
		return m, tea.Quit
	case "c":
		m.mode, m.action = modeConfirm, actClose
	default:
		m.mode = modeList
	}
	return m, nil
}

func (m model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeList
	case "enter":
		q := strings.TrimSpace(m.input)
		m.mode = modeList
		if q == "" {
			return m, nil
		}
		m.notice = "searching " + humanSize(totalBytes(m.all)) + " of transcripts…"
		sessions := m.all
		return m, func() tea.Msg { return searchMsg{q, searchTranscripts(sessions, q)} }
	case "backspace":
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.input += msg.String()
		}
	}
	return m, nil
}

func totalBytes(sessions []*session) int64 {
	var n int64
	for _, s := range sessions {
		n += s.Bytes
	}
	return n
}

// helpLines is the reference page behind ?. It opens with what the tool is
// for and how to read a row, because the keys only make sense once those land.
func helpLines() []string {
	key := func(k, text string) string {
		return "   " + stYellow.Render(fmt.Sprintf("%-9s", k)) + stDim.Render(text)
	}
	head := func(text string) string { return " " + stBold.Render(text) }

	return []string{
		"",
		head("cs \u2014 every Claude Code session on this machine, newest first."),
		"",
		stDim.Render("   Claude's own picker starts from the directory you are standing in."),
		stDim.Render("   This lists all of them, and shows the last thing you typed in each,"),
		stDim.Render("   so you can see where you stopped before deciding what to reopen."),
		"",
		head("Reading a row"),
		"",
		"    3  " + stBold.Render("\u2605 Trace the retry loop in the upload worker"),
		"       " + stCyan.Render("~/work/api-gateway") + stDim.Render(" \u00b7 main    first 04-08 14:26 \u00b7 last 04-09 11:00 \u00b7 144 turns \u00b7 ") + stGreen.Render("\u25cflive"),
		"       " + stDim.Render("\u21b3 yes, proceed"),
		"       " + stDim.Render("2 MB \u00b7 \u270e queue/retry.go ") + stPlus.Render("+185") + stDim.Render("/") + stMinus.Render("-42") + stDim.Render(" \u00b7 queue/worker.go ") + stPlus.Render("+133"),
		"",
		stDim.Render("   Line 1 is Claude's own title, with \u2605 if you bookmarked it."),
		stDim.Render("   Line 2 is where it ran and when. Line 3 is your last prompt."),
		stDim.Render("   Line 4 appears on the highlighted row: disk size, then the files"),
		stDim.Render("   it changed with line counts, biggest edit first."),
		"",
		stDim.Render("   ") + stGreen.Render("\u25cflive") + stDim.Render(" means the process is genuinely still running \u2014 its pid"),
		stDim.Render("   exists and the start time matches. It is not a self-reported label,"),
		stDim.Render("   so it cannot go stale the way \"working\" or \"needs input\" can."),
		"",
		head("Three views"),
		key("", "the timeline, and two others you toggle into and out of:"),
		key("T", "trash \u2014 discarded sessions, still recoverable"),
		key("P", "bookmarks \u2014 starred sessions, at any age and any directory"),
		"",
		stDim.Render("   Shift opens a view, the plain letter is the verb that puts things"),
		stDim.Render("   in it: t trashes, p bookmarks. Inside the view the same letter"),
		stDim.Render("   takes them back out. esc returns to the timeline, as does"),
		stDim.Render("   pressing the same shifted letter again."),
		"",
		stDim.Render("   The footer always lists the keys that do something where you are."),
		"",
		head("Moving"),
		key("j k \u2191 \u2193", "one row      ctrl+u ctrl+d   half a page"),
		key("g G", "first, last"),
		"",
		head("Opening"),
		key("enter", "resume it, in the directory it ran in"),
		key("n", "start a new session \u2014 suggests directories, creates one if needed"),
		key("f", "resume as a fork, leaving the original transcript alone"),
		key("v", "replay every prompt you typed in it"),
		key("R", "rename \u2014 the new name shows in claude --resume too"),
		"",
		head("Choosing what to act on"),
		key("space", "mark this row, move down"),
		key("A", "mark everything currently in view"),
		key("ctrl+a", "mark just the throwaways: under 3 turns, or under /tmp"),
		key("esc", "unwind one layer: marks, search, filter, then the view you"),
		key("", "are in, and finally quit"),
		"",
		stDim.Render("   t, p, c and D act on every marked session, or on the highlighted"),
		stDim.Render("   one when nothing is marked. Destructive keys confirm first."),
		"",
		head("Acting"),
		key("t", "on the timeline, send it to the trash"),
		key("t", "inside the trash, put it back where it came from"),
		key("p", "bookmark it, or remove the bookmark"),
		key("c", "close: SIGTERM, then SIGKILL if ignored. Transcript kept."),
		"",
		head("Only inside the trash"),
		key("D", "delete the marked or highlighted sessions for good"),
		key("ctrl+t", "empty the trash completely"),
		stDim.Render("   These two are the only steps in the tool that lose anything."),
		"",
		head("Narrowing"),
		key("/", "filter on title, path, branch and last prompt"),
		key("ctrl+/", "search the transcript bodies themselves"),
		key("w", "time window: today, 7d, 30d, 90d, all"),
		key("h", "only this directory, or everywhere"),
		key("s", "sort: recent, size, turns, project"),
		"",
		head("Housekeeping"),
		key("r", "rescan everything from disk"),
		key("ctrl+g", "drop session records whose process is gone"),
		"",
		head("Where things live"),
		key("", "~/.claude/projects/*/*.jsonl   the transcripts this reads"),
		key("", "~/.claude/cs-prefs.json        scope, sort, directory"),
		key("", "~/.claude/cs-pins.json         bookmarks"),
		key("", "~/.claude/.cs-trash            discarded sessions"),
		"",
	}
}

func (m model) viewHelp() string {
	body := m.h - 2
	lines := helpLines()
	at := min(m.detailAt, max(0, len(lines)-body))
	view := lines[at:]
	if len(view) > body {
		view = view[:body]
	}

	place := ""
	if sb := scrollBar(at, body, len(lines), 24); sb != "" {
		place = sb
	}
	return barSplit(stBar, m.w, "Keys", place) + "\n" + strings.Join(view, "\n") +
		strings.Repeat("\n", max(1, body-len(view)+1)) +
		bar(stBarKeys, m.w, "jk scroll · any other key returns")
}

func (m model) View() string {
	switch m.mode {
	case modeDetail:
		return m.viewDetail()
	case modeHelp:
		return m.viewHelp()
	case modeNew:
		return m.viewNew()
	}

	body := m.h - 2
	lines, _, under := m.renderList()
	if len(m.shown) == 0 {
		lines = m.emptyState()
	}

	off := min(m.offset, max(0, len(lines)-body))
	view := lines
	if off < len(view) {
		view = view[off:]
	} else {
		view = nil
	}
	if len(view) > body {
		view = view[:body]
	}
	// Pin the heading of whatever group the top row belongs to; scrolling into
	// a long project otherwise leaves rows with no visible label.
	if len(view) > 0 && off > 0 && off < len(under) && under[off] < off {
		view[0] = lines[under[off]] + stDim.Render("  ↑")
	}

	var bytes int64
	for _, s := range m.shown {
		bytes += s.Bytes
	}
	headStyle, title := stBar, ""
	switch {
	case m.trashView:
		headStyle = stBarTrash
		title = fmt.Sprintf("IN TRASH  ·  %d discarded  ·  %s", len(m.shown), humanSize(bytes))
	case m.pinsOnly:
		headStyle = stBarPins
		title = fmt.Sprintf("IN BOOKMARKS  ·  %d starred  ·  %s", len(m.shown), humanSize(bytes))
	default:
		title = fmt.Sprintf("%d sessions  ·  %s  ·  %s", len(m.shown), m.scopeLabel(), humanSize(bytes))
		if m.hereOnly {
			title += "  ·  " + tilde(m.cwd)
		}
	}
	if m.sortBy != sortRecent {
		title += "  ·  by " + sortNames[m.sortBy]
	}
	if m.hitQuery != "" {
		title += "  ·  mentions " + strconv.Quote(m.hitQuery)
	}
	if len(m.marked) > 0 {
		title += fmt.Sprintf("  ·  %d marked", len(m.marked))
	}
	if m.query != "" {
		title += "  ·  /" + m.query
	}
	place := ""
	if len(m.shown) > 0 {
		place = fmt.Sprintf("%d/%d", m.cursor+1, len(m.shown))
	}
	if sb := scrollBar(off, body, len(lines), 24); sb != "" {
		place += "  " + sb
	}
	head := barSplit(headStyle, m.w, title, place)

	foot := bar(stBarKeys, m.w, m.footerKeys())
	if m.notice != "" {
		foot = bar(stBarIn, m.w, m.notice)
	}
	switch {
	case m.mode == modeRename:
		foot = bar(stBarIn, m.w, "rename: "+m.input+"█")
	case m.mode == modeFilter:
		foot = bar(stBarIn, m.w, "filter: "+m.input+"█")
	case m.mode == modeSearch:
		foot = bar(stBarIn, m.w, "search transcripts: "+m.input+"█")
	case m.mode == modeLive:
		foot = bar(stBarWarn, m.w, m.livePrompt())
	case m.mode == modeConfirm:
		foot = bar(stBarWarn, m.w, m.confirmPrompt())
	case m.err != "":
		foot = bar(stBarErr, m.w, m.err)
	}
	return head + "\n" + strings.Join(view, "\n") +
		strings.Repeat("\n", max(1, body-len(view)+1)) + foot
}

func (m model) livePrompt() string {
	s := m.sel()
	return fmt.Sprintf("Already running as pid %d.  [f] fork it  ·  [r] resume anyway, two writers  ·  [c] close it  ·  [esc]", s.Pid)
}

// restoreTargets pulls the marked (or highlighted) sessions back out of the
// trash. It is the same verb as sending them there, which is why both sit on t.
func (m *model) restoreTargets() {
	n := 0
	for _, t := range m.targets() {
		if err := restore(t.ID); err != nil {
			m.err = err.Error()
		} else {
			n++
		}
	}
	m.all, m.trashAll = collect(), trashSessions()
	m.marked = map[string]bool{}
	m.apply()
	m.cursor = min(m.cursor, max(0, len(m.shown)-1))
	m.notice = fmt.Sprintf("restored %d", n)
}

// footerKeys lists what actually does something in the current view. The side
// views share the movement and marking keys but almost none of the verbs, so
// showing the timeline's set there would be mostly wrong.
func (m model) footerKeys() string {
	switch {
	case m.trashView:
		return "? keys · t restore · D delete for good · ctrl+t empty the trash · space mark · esc back to timeline"
	case m.pinsOnly:
		return "? keys · enter resume · p unbookmark · v prompts · space mark · esc back to timeline · q quit"
	}
	return "? keys · enter resume · n new · space mark · t trash it · p bookmark · c close · T trash · P bookmarks · q quit"
}

// emptyState explains an empty list. Every filter that can empty it also has a
// key that undoes it, so the way out is named rather than left to be guessed.
func (m model) emptyState() []string {
	var what, how string
	switch {
	case m.trashView && m.hits == nil && m.query == "":
		what, how = "The trash is empty.", "esc returns to the timeline"
	case m.hits != nil:
		what, how = fmt.Sprintf("No session mentions %q.", m.hitQuery), "esc clears the search"
	case m.query != "":
		what, how = fmt.Sprintf("Nothing matches %q.", m.query), "esc clears the filter"
	case m.pinsOnly:
		what, how = "No bookmarks yet.", "esc returns to the timeline; p bookmarks the highlighted session"
	case m.hereOnly:
		what, how = "No sessions started in "+tilde(m.cwd)+".", "h looks everywhere, w widens the time window"
	default:
		what, how = "No sessions in the last "+m.scopeLabel()+".", "w widens the time window"
	}
	return []string{"", "  " + stBold.Render(what), "", "  " + stDim.Render(how)}
}

// confirmPrompt spells out exactly what is about to happen, including how many
// of the targets are running, since only those get a signal.
func (m model) confirmPrompt() string {
	targets := m.targets()
	live := 0
	for _, s := range targets {
		if s.Live {
			live++
		}
	}

	what := fmt.Sprintf("%d sessions", len(targets))
	if len(targets) == 1 {
		what = trunc(targets[0].Title, 44)
		if targets[0].Title == "" {
			what = "(untitled)"
		}
	}
	if m.action == actClose {
		if live == 0 {
			return "Nothing to close — none of the targets are running.  [n]"
		}
		return fmt.Sprintf("Close %s?  SIGTERM %d running, transcripts kept.  [y/n]", what, live)
	}
	var bytes int64
	for _, s := range targets {
		bytes += s.Bytes
	}

	switch m.action {
	case actEmpty:
		var all int64
		for _, s := range m.trashAll {
			all += s.Bytes
		}
		return fmt.Sprintf("Empty the trash?  %d sessions, %s, gone for good.  [y/n]",
			len(m.trashAll), humanSize(all))
	case actPurge:
		return fmt.Sprintf("Delete %s for good?  %s, not recoverable.  [y/n]", what, humanSize(bytes))
	}

	extra := ""
	if live > 0 {
		extra = fmt.Sprintf("%d running stopped first, ", live)
	}
	return fmt.Sprintf("Send %s to the trash?  %s%s, recoverable with T.  [y/n]",
		what, extra, humanSize(bytes))
}

func (m model) viewDetail() string {
	s := m.sel()
	var lines []string
	for _, p := range m.prompts {
		lines = append(lines, "")
		stamp := stYellow.Render(p.At.Format("01-02 15:04"))
		for i, l := range strings.Split(strings.TrimSpace(p.Text), "\n") {
			for _, chunk := range wrap(l, max(20, m.w-16)) {
				if i == 0 && len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
					lines[len(lines)-1] = "  " + stamp + "  " + chunk
					i++
					continue
				}
				lines = append(lines, "                 "+chunk)
			}
		}
	}
	if len(lines) == 0 {
		lines = []string{"", stDim.Render("  no recorded prompts")}
	}

	body := m.h - 2
	at := min(m.detailAt, max(0, len(lines)-body))
	view := lines[at:]
	if len(view) > body {
		view = view[:body]
	}
	head := bar(stBar, m.w, s.Title+"  ·  "+tilde(s.Cwd))
	foot := bar(stBarKeys, m.w, fmt.Sprintf("%d prompts · jk scroll · enter resume · esc back", len(m.prompts)))
	return head + "\n" + strings.Join(view, "\n") +
		strings.Repeat("\n", max(1, body-len(view)+1)) + foot
}

func wrap(s string, n int) []string {
	r := []rune(s)
	if len(r) <= n {
		return []string{s}
	}
	var out []string
	for len(r) > n {
		cut := n
		for i := n; i > n/2; i-- {
			if r[i] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = r[cut:]
	}
	return append(out, string(r))
}

// stamp writes a labelled timestamp at a fixed width, so the columns hold
// still down the list. The date is always present: a session opened yesterday
// afternoon and touched again this morning otherwise reads as running
// backwards.
func stamp(label string, t time.Time) string {
	return label + " " + t.Format("01-02 15:04")
}
