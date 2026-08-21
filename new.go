package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// dirSuggest is one offered starting directory and the reason it is offered.
type dirSuggest struct {
	Path string
	Why  string
}

// suggestDirs offers the handful of directories worth starting in: the ones
// worked in most recently, then the ones worked in most often. A directory
// that is already in the recent list is not repeated among the common ones.
func suggestDirs(sessions []*session) []dirSuggest {
	type stat struct {
		count int
		last  int
	}
	seen := map[string]*stat{}
	var order []string
	for i, s := range sessions {
		st := seen[s.Cwd]
		if st == nil {
			st = &stat{last: i}
			seen[s.Cwd], order = st, append(order, s.Cwd)
		}
		st.count++
	}

	byRecent := append([]string(nil), order...)
	sort.SliceStable(byRecent, func(i, j int) bool {
		return seen[byRecent[i]].last < seen[byRecent[j]].last
	})

	out, taken := []dirSuggest{}, map[string]bool{}
	for _, p := range byRecent {
		if len(out) == 5 {
			break
		}
		out, taken[p] = append(out, dirSuggest{p, "recent"}), true
	}

	byCount := append([]string(nil), order...)
	sort.SliceStable(byCount, func(i, j int) bool {
		return seen[byCount[i]].count > seen[byCount[j]].count
	})
	for _, p := range byCount {
		if len(out) == 10 {
			break
		}
		if taken[p] || seen[p].count < 2 {
			continue
		}
		out = append(out, dirSuggest{p, fmt.Sprintf("%d sessions", seen[p].count)})
	}
	return out
}

func expandPath(p string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// completePath extends a partially typed directory as far as it can go
// unambiguously, the way a shell would.
func completePath(in string) string {
	full := expandPath(in)
	// Split on the last separator by hand. filepath.Dir and Base clean the
	// path as they go, which would eat a typed "." and make directories like
	// ~/.claude impossible to complete into.
	cut := strings.LastIndex(full, "/")
	if cut < 0 {
		return in
	}
	dir, base := full[:cut], full[cut+1:]
	if dir == "" {
		dir = "/"
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return in
	}
	wantHidden := strings.HasPrefix(base, ".")
	var hits []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, base) {
			continue
		}
		if strings.HasPrefix(name, ".") && !wantHidden {
			continue
		}
		hits = append(hits, name)
	}
	if len(hits) == 0 {
		return in
	}

	common := hits[0]
	for _, h := range hits[1:] {
		for !strings.HasPrefix(h, common) {
			common = common[:len(common)-1]
		}
	}
	out := strings.TrimSuffix(dir, "/") + "/" + common
	if len(hits) == 1 {
		out += "/"
	}
	return tilde(out)
}

const (
	newWhere = iota
	newPrompt
)

func (m model) updateNew(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.newStep == newPrompt {
		switch msg.String() {
		case "esc":
			m.newStep = newWhere
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			m.newDir = expandPath(strings.TrimSpace(m.input))
			return m, tea.Quit
		case "backspace":
			if r := []rune(m.newText); len(r) > 0 {
				m.newText = string(r[:len(r)-1])
			}
		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.newText += msg.String()
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = modeList
	case "up":
		m.newPick = max(-1, m.newPick-1)
		if m.newPick >= 0 {
			m.input = tilde(m.newSuggest[m.newPick].Path)
		}
	case "down":
		m.newPick = min(len(m.newSuggest)-1, m.newPick+1)
		if m.newPick >= 0 {
			m.input = tilde(m.newSuggest[m.newPick].Path)
		}
	case "tab":
		m.input, m.newPick = completePath(m.input), -1
	case "enter":
		if strings.TrimSpace(m.input) != "" {
			m.newStep = newPrompt
		}
	case "backspace":
		if r := []rune(m.input); len(r) > 0 {
			m.input, m.newPick = string(r[:len(r)-1]), -1
		}
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.input, m.newPick = m.input+msg.String(), -1
		}
	}
	return m, nil
}

func (m model) viewNew() string {
	body := m.h - 2
	lines := []string{""}

	if m.newStep == newWhere {
		lines = append(lines, " "+stBold.Render("Start a new session in"), "")
		for i, sg := range m.newSuggest {
			row := fmt.Sprintf("   %-46s %s", tilde(sg.Path), stDim.Render(sg.Why))
			if i == m.newPick {
				row = rowStyles(true).title.Render(" ▸ "+fmt.Sprintf("%-46s", tilde(sg.Path))) +
					rowStyles(true).dim.Render(" "+sg.Why)
			}
			lines = append(lines, row)
		}

		note := ""
		if p := expandPath(strings.TrimSpace(m.input)); p != "" {
			if _, err := os.Stat(p); err != nil {
				note = stYellow.Render("  will be created")
			}
		}
		lines = append(lines, "",
			" "+stBold.Render("Path")+"  "+stCyan.Render(m.input)+stSel.Render("█")+note)
	} else {
		dir := expandPath(strings.TrimSpace(m.input))
		note := ""
		if _, err := os.Stat(dir); err != nil {
			note = stYellow.Render("   (mkdir -p)")
		}
		lines = append(lines,
			" "+stBold.Render("New session in ")+stCyan.Render(tilde(dir))+note, "",
			" "+stBold.Render("Prompt")+"  "+m.newText+stSel.Render("█"), "",
			stDim.Render("   Leave it blank to land at an empty prompt."))
	}

	if len(lines) > body {
		lines = lines[:body]
	}
	foot := "↑↓ pick a suggestion · tab completes · enter continues · esc cancels"
	if m.newStep == newPrompt {
		foot = "enter launches · esc goes back"
	}
	return bar(stBar, m.w, "New session") + "\n" + strings.Join(lines, "\n") +
		strings.Repeat("\n", max(1, body-len(lines)+1)) + bar(stBarKeys, m.w, foot)
}
