package app

import (
	"errors"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/gitcmd"
	"github.com/nickooan/ntee-editor/internal/input"
)

// blameAuthorCap caps the author column so one long name can't eat the pane.
const blameAuthorCap = 16

// blameUncommittedLabel is the dimmed placeholder for lines not in any commit
// (unsaved buffer edits, staged-but-uncommitted changes, new files).
const blameUncommittedLabel = "uncommitted"

// zeroSHA is git blame's marker for lines with no commit.
const zeroSHA = "0000000000000000000000000000000000000000"

// blameRow annotates one buffer line. The index into m.blameRows IS the
// buffer line — blame rows map 1:1, so Esc/click/Ctrl+J need no row mapping.
type blameRow struct {
	author      string // "" for uncommitted
	date        string // "YYYY-MM-DD", "" for uncommitted
	group       int    // contiguous same-commit run id (Shift+↑/↓ jump target)
	uncommitted bool   // zero-SHA line (unsaved edit / not committed)
}

// blameReadyMsg delivers a computed blame from the worker goroutine. err is a
// user-facing message ("" on success); gen/rel guard against stale results.
type blameReadyMsg struct {
	gen     int
	rel     string
	rows    []blameRow
	authorW int  // precomputed min(longest author, blameAuthorCap)
	newFile bool // untracked path or empty repo: everything uncommitted
	err     string
}

// enterBlame switches to blame mode and kicks off the async blame
// computation. Errors keep the current mode (the @exec bar) so the user can
// correct the input.
func (m Model) enterBlame() (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.errText = "no file open"
		return m, nil
	}
	if _, _, ok, loading := m.gitScopeFor(m.openRel); !ok {
		m.errText = gitScopeError(loading)
		return m, nil
	}
	m = m.clearBlameState()
	m.edit.clearSelection() // a stale selection would hijack Ctrl+J's token
	m.blameGen++
	m.blameLoading = true
	m.mode = modeBlame
	return m, m.computeBlameCmd()
}

// clearBlameState drops the blame view model and resets every blame field
// except blameGen, which stays monotonic so in-flight results remain
// identifiable as stale.
func (m Model) clearBlameState() Model {
	m.blameRows = nil
	m.blameCursor, m.blameCx, m.blameScrollY = 0, 0, 0
	m.blameAuthorW = 0
	m.blameNewFile = false
	m.blameLoading = false
	m.blameHasPending = false
	m.blamePendingCursor, m.blamePendingScroll = 0, 0
	return m
}

// computeBlameCmd snapshots the buffer and runs git blame off the UI
// goroutine (precedent: computeDiffCmd). --contents=- blames the buffer, not
// the worktree file, so unsaved edits surface as uncommitted lines — the same
// "the buffer is the truth" rule diff mode follows.
func (m Model) computeBlameCmd() tea.Cmd {
	gen, rel := m.blameGen, m.openRel
	root, repoRel, _, _ := m.gitScopeFor(rel)
	cur := append([]string(nil), m.edit.lines...)
	return func() tea.Msg {
		msg := blameReadyMsg{gen: gen, rel: rel}
		if _, err := gitcmd.Out(root, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
			// A repo with no commits yet: not an error, everything is new.
			msg.newFile = true
		}
		var rows []blameRow
		if !msg.newFile {
			out, err := gitcmd.OutIn(root, []byte(strings.Join(cur, "\n")),
				"blame", "--porcelain", "--contents=-", "--", repoRel)
			switch {
			case err != nil && strings.Contains(firstStderrLine(err), "no such path"):
				msg.newFile = true // path absent in HEAD: new file
			case err != nil:
				msg.err = "git blame failed"
				if line := firstStderrLine(err); line != "" {
					msg.err = "git blame: " + line
				}
				return msg
			default:
				rows, err = parseBlamePorcelain(out)
				if err != nil {
					msg.err = "git blame: " + err.Error()
					return msg
				}
			}
		}
		if msg.newFile {
			rows = make([]blameRow, len(cur))
			for i := range rows {
				rows[i].uncommitted = true
			}
		}
		// Length reconciliation: git counts \n-terminated lines while the
		// buffer keeps a trailing "" element, so the parse usually comes back
		// one row short — pad the tail as uncommitted, clamp any excess.
		if pad := lastBlameGroup(rows) + 1; len(rows) < len(cur) {
			for len(rows) < len(cur) {
				rows = append(rows, blameRow{uncommitted: true, group: pad})
			}
		}
		msg.rows = rows[:len(cur)]
		msg.authorW = blameAuthorWidth(msg.rows)
		return msg
	}
}

// lastBlameGroup is the group id of the final row, -1 for an empty slice.
func lastBlameGroup(rows []blameRow) int {
	if len(rows) == 0 {
		return -1
	}
	return rows[len(rows)-1].group
}

// parseBlamePorcelain converts `git blame --porcelain` output into per-line
// rows. Porcelain announces every output line with a "<sha> <orig> <final>"
// header, sends each commit's attributes (author, author-time, …) only on the
// sha's FIRST appearance, and terminates every entry with the content line,
// which starts with a tab. Attributes are cached per sha so later repeats
// resolve. Zero-sha lines are uncommitted; git's placeholder authors for them
// ("Not Committed Yet", "External file (--contents)") are never surfaced.
func parseBlamePorcelain(out []byte) ([]blameRow, error) {
	var rows []blameRow
	meta := map[string]blameRow{}
	cur := ""
	group := -1
	prevSHA := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "\t") {
			if cur == "" {
				return nil, errors.New("unexpected porcelain output")
			}
			if cur != prevSHA {
				group++
				prevSHA = cur
			}
			row := meta[cur]
			row.group = group
			rows = append(rows, row)
			continue
		}
		key, rest, _ := strings.Cut(line, " ")
		switch {
		case len(key) == 40 && isHexString(key) && restStartsWithDigit(rest):
			cur = key
			if _, ok := meta[cur]; !ok {
				meta[cur] = blameRow{uncommitted: cur == zeroSHA}
			}
		case key == "author" && cur != "" && cur != zeroSHA:
			row := meta[cur]
			row.author = rest
			meta[cur] = row
		case key == "author-time" && cur != "" && cur != zeroSHA:
			if n, err := strconv.ParseInt(rest, 10, 64); err == nil {
				row := meta[cur]
				// Local time, date-only precision: honoring author-tz isn't
				// worth the code for a day-granularity display.
				row.date = time.Unix(n, 0).Format("2006-01-02")
				meta[cur] = row
			}
		}
	}
	return rows, nil
}

// isHexString reports whether s is entirely lowercase-hex characters.
func isHexString(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return len(s) > 0
}

// restStartsWithDigit distinguishes an entry header ("<sha> 12 34") from a
// hypothetical attribute whose key happens to be 40 hex chars.
func restStartsWithDigit(rest string) bool {
	return rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// blameAuthorWidth is the author column's rune width: the longest author name
// (the uncommitted placeholder participates), capped at blameAuthorCap.
func blameAuthorWidth(rows []blameRow) int {
	w := 1
	for _, row := range rows {
		name := row.author
		if row.uncommitted {
			name = blameUncommittedLabel
		}
		if n := len([]rune(name)); n > w {
			w = n
		}
	}
	return min(w, blameAuthorCap)
}

// handleBlameReady lands the async blame. A generation, file, or mode
// mismatch means the user Esc'd, re-ran, or switched files while it computed
// — drop it.
func (m Model) handleBlameReady(msg blameReadyMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.blameGen || msg.rel != m.openRel || m.mode != modeBlame {
		return m, nil
	}
	if msg.err != "" {
		m.errText = msg.err
		m.mode = modeEdit
		return m.clearBlameState(), nil
	}
	m.blameLoading = false
	m.blameRows = msg.rows
	m.blameAuthorW = msg.authorW
	m.blameNewFile = msg.newFile
	h := m.contentHeight() + 1 // rendered rows: single-line status, like edit mode
	if m.blameHasPending {
		// Ctrl+O return: restore the remembered position, clamped — the
		// buffer may have shrunk while we were away.
		m.blameHasPending = false
		m.blameCursor = input.Clamp(m.blamePendingCursor, 0, len(m.blameRows)-1)
		m.blameScrollY = input.Clamp(m.blamePendingScroll, 0, max(0, len(m.blameRows)-h))
	} else {
		// Fresh entry: stay at the current place — same buffer line, same
		// on-screen row. The exact inverse of the Esc mapping in exitBlame.
		screenRow := m.edit.cy - fileViewportTop(m.edit.cy, m.fileScrollY, h, len(m.edit.lines))
		m.blameCursor = input.Clamp(m.edit.cy, 0, len(m.blameRows)-1)
		m.blameScrollY = input.Clamp(m.blameCursor-screenRow, 0, max(0, len(m.blameRows)-h))
	}
	m.blameCx = input.Clamp(m.edit.cx, 0, len([]rune(m.blameRowText(m.blameCursor))))
	return m, nil
}

// blameRowText is the display text of a row — the live buffer line (rows map
// 1:1), clamped so callers can feed any cursor value.
func (m Model) blameRowText(i int) string {
	if len(m.edit.lines) == 0 {
		return ""
	}
	return m.edit.lines[input.Clamp(i, 0, len(m.edit.lines)-1)]
}
