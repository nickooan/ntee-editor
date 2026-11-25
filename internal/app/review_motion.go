package app

import (
	"fmt"
	"math"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/diff"
)

// reviewMotion is the review page's animations: rows injected into the left
// list, and the selected file's code change. frame advances per tick only
// while one runs; nothing ticks on an idle review.
type reviewMotion struct {
	frame   int
	ticking bool
	// injected maps a left-pane row's path to the frame its inject started.
	// Replaced, never mutated: Model copies share it.
	injected map[string]int
	code     *codeMotion
}

// codeMotion is the selected file's change animation: rows expanding in,
// removed rows collapsing out.
type codeMotion struct {
	path       string
	startFrame int
	rows       *rowMotion
}

// rowMotion is what changed between two versions of a file's diff document,
// in the newer document's rows. ghosts are the removed rows' texts, keyed by
// the newer row they sat before (len(rows) = past the last row).
type rowMotion struct {
	inserted  []bool
	recolored []bool
	ghosts    map[int][]string
}

// diffMotionFrame is one frame of a rowMotion as renderDiffDocument draws
// it. The zero value (or progress 1) draws the document as it is.
type diffMotionFrame struct {
	rows     *rowMotion
	progress float64
}

func (frame diffMotionFrame) active() bool {
	return frame.rows != nil && frame.progress < 1
}

const (
	reviewMotionTickInterval = 30 * time.Millisecond
	reviewInjectFrames       = 6 // ~180ms: a row sliding into the left list
	reviewCodeFrames         = 5 // ~150ms: code expanding in / collapsing out
	// maxMotionRows skips comparing huge documents: the animation is a nicety,
	// the diff of two 20k-row documents is not free.
	maxMotionRows = 20000
	// maxGhostRows caps how many rows a removed block collapses from, so a big
	// deletion doesn't push the window around.
	maxGhostRows = 6
)

type reviewMotionTickMsg struct {
	gen int
}

func reviewMotionTick(gen int) tea.Cmd {
	return tea.Tick(reviewMotionTickInterval, func(time.Time) tea.Msg { return reviewMotionTickMsg{gen: gen} })
}

// progress is how far an animation of length frames that started at start
// is, from 0 to 1.
func (motion reviewMotion) progress(start, length int) float64 {
	return math.Min(1, math.Max(0, float64(motion.frame-start)/float64(length)))
}

func (motion reviewMotion) running() bool {
	for _, start := range motion.injected {
		if motion.frame-start < reviewInjectFrames {
			return true
		}
	}
	return motion.code != nil && motion.frame-motion.code.startFrame < reviewCodeFrames
}

// withReviewMotionTick starts the review's tick chain once an animation
// runs, one chain at a time.
func withReviewMotionTick(model tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m, ok := model.(Model)
	if !ok || m.mode != modeReview || m.review.motion.ticking || !m.review.motion.running() {
		return model, cmd
	}
	m.review.motion.ticking = true
	return m, tea.Batch(cmd, reviewMotionTick(m.review.gen))
}

// handleReviewMotionTick advances the animations, drops the finished ones,
// and ends the chain once none runs.
func (m Model) handleReviewMotionTick(msg reviewMotionTickMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.review.gen || m.mode != modeReview {
		return m, nil
	}
	motion := m.review.motion
	motion.frame++
	if len(motion.injected) > 0 {
		injected := make(map[string]int, len(motion.injected))
		for path, start := range motion.injected {
			if motion.frame-start < reviewInjectFrames {
				injected[path] = start
			}
		}
		motion.injected = injected
	}
	if motion.code != nil && motion.frame-motion.code.startFrame >= reviewCodeFrames {
		motion.code = nil
	}
	if !motion.running() {
		motion.ticking = false
		m.review.motion = motion
		return m, nil
	}
	m.review.motion = motion
	return m, reviewMotionTick(m.review.gen)
}

// injectReviewRows starts the inject animation of every left-pane row the
// last tree rebuild added — a new file and any new folders above it.
func (m Model) injectReviewRows(previousRows map[string]int) Model {
	var added []string
	for _, entry := range m.review.entries {
		if _, existed := previousRows[entry.RelativePath]; !existed {
			added = append(added, entry.RelativePath)
		}
	}
	if len(added) == 0 {
		return m
	}
	injected := make(map[string]int, len(m.review.motion.injected)+len(added))
	for path, start := range m.review.motion.injected {
		injected[path] = start
	}
	for _, path := range added {
		injected[path] = m.review.motion.frame
	}
	m.review.motion.injected = injected
	return m
}

// reviewInjectProgress is how far the row at path is into its inject, and
// whether it is injecting at all.
func (m Model) reviewInjectProgress(path string) (float64, bool) {
	start, ok := m.review.motion.injected[path]
	if !ok {
		return 1, false
	}
	progress := m.review.motion.progress(start, reviewInjectFrames)
	return progress, progress < 1
}

// startCodeMotion animates rows on the selected file, which just loaded.
func (m Model) startCodeMotion(path string, rows *rowMotion) Model {
	if rows == nil {
		return m
	}
	m.review.motion.code = &codeMotion{path: path, startFrame: m.review.motion.frame, rows: rows}
	return m
}

// reviewCodeFrame is the selected file's current change-animation frame.
func (m Model) reviewCodeFrame(file reviewFile) diffMotionFrame {
	code := m.review.motion.code
	if code == nil || code.path != file.workspaceRel {
		return diffMotionFrame{}
	}
	return diffMotionFrame{rows: code.rows, progress: m.review.motion.progress(code.startFrame, reviewCodeFrames)}
}

// revealMotion is a first-shown file's animation: every row comes in, top
// to bottom.
func revealMotion(document diffDocument) *rowMotion {
	if len(document.rows) == 0 || len(document.rows) > maxMotionRows {
		return nil
	}
	inserted := make([]bool, len(document.rows))
	for index := range inserted {
		inserted[index] = true
	}
	return &rowMotion{inserted: inserted}
}

// compareReviewDocuments finds what changed between the version of a file's
// diff the user saw and the newer one: rows new to it, rows that only changed
// color (a context line now removed, say), and rows gone from it. It runs on
// the load goroutine; nil means nothing to animate.
func compareReviewDocuments(previous, next diffDocument) *rowMotion {
	if len(next.rows) == 0 || len(previous.rows) > maxMotionRows || len(next.rows) > maxMotionRows {
		return nil
	}
	previousTexts := make([]string, len(previous.rows))
	for index := range previous.rows {
		previousTexts[index] = previous.rowText(index)
	}
	nextTexts := make([]string, len(next.rows))
	for index := range next.rows {
		nextTexts[index] = next.rowText(index)
	}
	motion := &rowMotion{inserted: make([]bool, len(next.rows)), recolored: make([]bool, len(next.rows))}
	changed := false
	var removed []string
	flush := func(beforeRow int) {
		if len(removed) == 0 {
			return
		}
		if motion.ghosts == nil {
			motion.ghosts = map[int][]string{}
		}
		motion.ghosts[beforeRow] = removed
		removed, changed = nil, true
	}
	for _, op := range diff.Lines(previousTexts, nextTexts) {
		switch op.Kind {
		case diff.Delete:
			removed = append(removed, previousTexts[op.AIdx])
		case diff.Insert:
			flush(op.BIdx)
			motion.inserted[op.BIdx], changed = true, true
		case diff.Equal:
			flush(op.BIdx)
			if previous.rows[op.AIdx].kind != next.rows[op.BIdx].kind {
				motion.recolored[op.BIdx], changed = true, true
			}
		}
	}
	flush(len(next.rows))
	if !changed {
		return nil
	}
	return motion
}

// blendHex mixes two #rrggbb colors, t=0 being from and t=1 to. t is
// quantized to eighths so the per-background style memos stay small.
func blendHex(from, to string, t float64) string {
	t = math.Round(math.Min(1, math.Max(0, t))*8) / 8
	fromRGB, okFrom := parseHexColor(from)
	toRGB, okTo := parseHexColor(to)
	if !okFrom || !okTo {
		return to
	}
	var mixed [3]int
	for channel := range mixed {
		mixed[channel] = int(math.Round(float64(fromRGB[channel]) + (float64(toRGB[channel])-float64(fromRGB[channel]))*t))
	}
	return fmt.Sprintf("#%02x%02x%02x", mixed[0], mixed[1], mixed[2])
}

func parseHexColor(hex string) ([3]int, bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return [3]int{}, false
	}
	value, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return [3]int{}, false
	}
	return [3]int{int(value >> 16 & 0xff), int(value >> 8 & 0xff), int(value & 0xff)}, true
}

// easeOut starts fast and settles: motion that feels snappy at its short
// length.
func easeOut(progress float64) float64 {
	return 1 - (1-progress)*(1-progress)
}
