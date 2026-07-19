package app

import (
	"path"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/syntax"
	"github.com/nickooan/ntee-editor/internal/view"
)

// Repo-wide content search (Ctrl+G): the corpus is snapshotted into memory
// when the overlay opens, so per-keystroke search never touches the disk.

type grepFile struct {
	rel   string
	lines []string
}

type grepHit struct {
	rel  string
	line int
}

// maxGrepResults caps the hit list per query.
const maxGrepResults = 200

// openGrep loads the corpus and opens the overlay. From edit mode with
// unsaved changes it asks the user to save first — Enter on a hit opens
// another file, which would silently discard the buffer.
func (m Model) openGrep() Model {
	if m.mode == modeEdit && m.edit.dirty {
		m.errText = "unsaved changes — save (Ctrl+S) before repo search"
		return m
	}
	sizeCap := m.cfg.Editor.MaxHighlightKB * 1024
	var files []grepFile
	for _, rel := range filetree.BuildAllEntries(m.root, m.cfg.Tree.Ignore, m.gitignore) {
		f, ok := filetree.ReadViewFile(m.root, rel)
		if !ok || f.Binary {
			continue
		}
		if sizeCap > 0 && len(f.Content) > sizeCap {
			continue
		}
		files = append(files, grepFile{rel: rel, lines: view.NormalizeLines(f.Content)})
	}
	m.grepOpen = true
	m.grepQuery = ""
	m.grepIndex = 0
	m.grepResults = nil
	m.grepFiles = files
	m.grepHlRel = ""
	m.grepHl = nil
	return m
}

// refreshGrep re-runs the search over the in-memory corpus. Same semantics as
// the in-file search: case-insensitive regex with a literal fallback.
func (m Model) refreshGrep() Model {
	m.grepResults = nil
	m.grepIndex = 0
	if len([]rune(m.grepQuery)) < 2 {
		return m
	}
	re := view.CreateSearchRegex(m.grepQuery)
	if re == nil {
		return m
	}
	for _, f := range m.grepFiles {
		for i, line := range f.lines {
			if re.MatchString(line) {
				m.grepResults = append(m.grepResults, grepHit{rel: f.rel, line: i})
				if len(m.grepResults) >= maxGrepResults {
					return m.refreshGrepPreview()
				}
			}
		}
	}
	return m.refreshGrepPreview()
}

// grepSelectedFile returns the corpus entry for the current selection.
func (m Model) grepSelectedFile() (grepFile, grepHit, bool) {
	if len(m.grepResults) == 0 {
		return grepFile{}, grepHit{}, false
	}
	hit := m.grepResults[input.Clamp(m.grepIndex, 0, len(m.grepResults)-1)]
	for _, f := range m.grepFiles {
		if f.rel == hit.rel {
			return f, hit, true
		}
	}
	return grepFile{}, grepHit{}, false
}

func (m Model) handleGrepKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.grepOpen = false
		m.grepFiles = nil
		m.grepHl = nil
	case tea.KeyUp:
		m.grepIndex = max(0, m.grepIndex-1)
		m = m.refreshGrepPreview()
	case tea.KeyDown:
		m.grepIndex = min(max(0, len(m.grepResults)-1), m.grepIndex+1)
		m = m.refreshGrepPreview()
	case tea.KeyEnter:
		_, hit, ok := m.grepSelectedFile()
		m.grepOpen = false
		m.grepFiles = nil
		m.grepHl = nil
		if !ok {
			break
		}
		m = m.openFileAt(hit.rel)
		if m.mode == modeEdit {
			m.edit.cy = input.Clamp(hit.line, 0, len(m.edit.lines)-1)
			m.edit.cx = 0
			m = m.anchorCursorLine()
		}
	case tea.KeyBackspace:
		if runes := []rune(m.grepQuery); len(runes) > 0 {
			m.grepQuery = string(runes[:len(runes)-1])
			m = m.refreshGrep()
		}
	case tea.KeySpace:
		m.grepQuery += " "
		m = m.refreshGrep()
	case tea.KeyRunes:
		m.grepQuery += string(msg.Runes)
		m = m.refreshGrep()
	}
	return m, nil
}

// refreshGrepPreview keeps the syntax highlighting of the selected hit's file
// cached (whole-buffer tokenize, re-run only when the selection changes file).
func (m Model) refreshGrepPreview() Model {
	f, _, ok := m.grepSelectedFile()
	if !ok {
		m.grepHlRel, m.grepHl = "", nil
		return m
	}
	if f.rel == m.grepHlRel {
		return m
	}
	m.grepHlRel = f.rel
	m.grepHl = syntax.HighlightLines(path.Base(f.rel), strings.Join(f.lines, "\n"))
	return m
}
