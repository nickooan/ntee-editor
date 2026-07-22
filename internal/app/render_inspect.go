package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/store"
)

// renderInspectMenu draws the inspection dashboard's left pane: a one-level
// menu styled like the file sidebar (selected row gets the selection bar).
func (m Model) renderInspectMenu(width, height int) string {
	rows := make([]string, 0, height)
	for i, item := range inspectMenuItems {
		label := padTo(truncateRunes(" "+item, width), width)
		if i == m.inspectMenu {
			rows = append(rows, selectedEntryStyle.Render(label))
		} else {
			rows = append(rows, dirStyle.Render(label))
		}
	}
	return strings.Join(rows, "\n")
}

// renderInspectMain draws the right info pane for the selected menu item.
func (m Model) renderInspectMain(width, height int) string {
	switch m.inspectMenu {
	case inspectMenuLSP:
		return m.renderInspectLSP(width)
	default:
		return m.renderInspectDB(width)
	}
}

func (m Model) renderInspectDB(width int) string {
	title := dirStyle.Render(truncateRunes("ntee-db store", width))
	switch {
	case m.inspectLoading:
		return title + "\n\n" + baseStyle.Render("gathering store statistics…")
	case errors.Is(m.inspectInfoErr, store.ErrNoStats):
		return title + "\n\n" + ignoredFileStyle.Render("in-memory store (persistence disabled) — no statistics")
	}

	// Labels form an aligned column: padded to the longest label plus a gap.
	const labelW = len("generations") + 2
	row := func(label, value string) string {
		return baseStyle.Render(padTo(label, labelW) + value)
	}

	info := m.inspectInfo
	dead := info.MainBytes - info.LiveBytes
	rows := []string{
		title,
		"",
		row("records", fmt.Sprintf("%d", info.Records)),
		row("main log", fmt.Sprintf("%s  (%s live, %s dead — %s)",
			humanBytes(info.MainBytes), humanBytes(info.LiveBytes), humanBytes(dead),
			percent(dead, info.MainBytes))),
	}
	if m.inspectInfoErr != nil {
		rows = append(rows, errStyle.Render(truncateRunes("blob scan failed: "+m.inspectInfoErr.Error(), width)))
	} else {
		rows = append(rows,
			row("blobs", fmt.Sprintf("%s  (%s live, %s orphaned — %s)",
				humanBytes(info.BlobTotalBytes), humanBytes(info.BlobLiveBytes),
				humanBytes(info.BlobOrphaned), percent(info.BlobOrphaned, info.BlobTotalBytes))))
		gen := row("generations", fmt.Sprintf("%d", info.Generations))
		if info.Generations > 1 {
			gen += errStyle.Render("  (stray file — run db relieve)")
		}
		rows = append(rows, gen)
	}
	if m.inspectBusy != "" {
		rows = append(rows, "", editingStyle.Render("db "+m.inspectBusy+" running…"))
	}
	rows = append(rows, "", hintStyle.Render("db compact drops dead records · db relieve also rewrites blobs"))
	return strings.Join(rows, "\n")
}

func (m Model) renderInspectLSP(width int) string {
	title := dirStyle.Render(truncateRunes("language servers", width))
	sts := m.lsp.Statuses()
	if len(sts) == 0 {
		return title + "\n\n" + ignoredFileStyle.Render(truncateRunes(
			"lsp disabled globally (lsp.enabled: false) — `lsp enable all` writes the config; restart ntee to apply", width))
	}

	nameW := 0
	for _, st := range sts {
		nameW = max(nameW, len(st.Lang))
	}
	rows := []string{title, ""}
	for _, st := range sts {
		name := fileStyle.Render(padTo(st.Lang, nameW+2))
		var state string
		switch st.State {
		case lsp.LangRunning:
			state = openFileStyle.Render("running")
		case lsp.LangStopped:
			state = uncommittedFileStyle.Render("stopped")
		default:
			state = ignoredFileStyle.Render("disabled")
			if st.Reason != "" {
				state += ignoredFileStyle.Render(truncateRunes(" — "+st.Reason, max(0, width-nameW-12)))
			}
		}
		rows = append(rows, name+state)
	}
	rows = append(rows, "", hintStyle.Render("stopped = starts on demand when a matching file opens"))
	return strings.Join(rows, "\n")
}

// humanBytes formats a byte count for the inspection pane (B/KB/MB/GB).
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// percent renders part/total as "N%", guarding the empty store.
func percent(part, total int64) string {
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", part*100/total)
}
