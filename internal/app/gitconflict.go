package app

import "strings"

// conflictBlock locates one git merge-conflict region within a line buffer by
// the line indices of its markers. A block spans:
//
//	start  <<<<<<< oursLabel
//	       …ours content…
//	base   ||||||| baseLabel   (diff3 only; -1 when absent)
//	       …base content…      (always discarded)
//	mid    =======
//	       …theirs content…
//	end    >>>>>>> theirsLabel
//
// It is a value describing positions only; resolution slices the original
// lines with these indices.
type conflictBlock struct {
	start, mid, end int
	base            int // ||||||| line (diff3), or -1
	oursLabel       string
	theirsLabel     string
}

// Conflict marker prefixes are exactly seven characters (git's fixed width).
const (
	markerOurs   = "<<<<<<<"
	markerBase   = "|||||||"
	markerMid    = "======="
	markerTheirs = ">>>>>>>"
)

// hasMarker reports whether line begins with a conflict marker: the seven-char
// prefix followed by end-of-line or a space (so a label can follow). Requiring
// the boundary avoids matching an eight-plus run or unrelated text.
func hasMarker(line, marker string) bool {
	if !strings.HasPrefix(line, marker) {
		return false
	}
	rest := line[len(marker):]
	return rest == "" || rest[0] == ' '
}

// markerLabel returns the text after a marker prefix (the branch/ref name),
// trimmed. Empty when the marker carries no label.
func markerLabel(line, marker string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, marker))
}

// findConflictBlocks scans lines and returns every well-formed conflict block
// in order. Malformed sequences (an unclosed <<<<<<<, a >>>>>>> before its
// =======, or a new <<<<<<< before the current one closes) are skipped rather
// than reported — this drives an edit action, not a linter.
func findConflictBlocks(lines []string) []conflictBlock {
	var blocks []conflictBlock
	for i := 0; i < len(lines); {
		if !hasMarker(lines[i], markerOurs) {
			i++
			continue
		}
		if blk, next, ok := parseBlockAt(lines, i); ok {
			blocks = append(blocks, blk)
			i = next
		} else {
			i++ // stray start: skip it and keep scanning
		}
	}
	return blocks
}

// parseBlockAt parses a single block whose <<<<<<< is at start. It returns the
// block and the index just past its >>>>>>> on success; ok is false for a
// malformed block (in which case the caller resumes scanning after start).
func parseBlockAt(lines []string, start int) (conflictBlock, int, bool) {
	base, mid := -1, -1
	for j := start + 1; j < len(lines); j++ {
		line := lines[j]
		switch {
		case hasMarker(line, markerOurs):
			return conflictBlock{}, 0, false // new start before this one closed
		case hasMarker(line, markerBase) && mid == -1 && base == -1:
			base = j
		case hasMarker(line, markerMid) && mid == -1:
			mid = j
		case hasMarker(line, markerTheirs):
			if mid == -1 {
				return conflictBlock{}, 0, false // closed before the separator
			}
			return conflictBlock{
				start:       start,
				base:        base,
				mid:         mid,
				end:         j,
				oursLabel:   markerLabel(lines[start], markerOurs),
				theirsLabel: markerLabel(lines[j], markerTheirs),
			}, j + 1, true
		}
	}
	return conflictBlock{}, 0, false // reached EOF unclosed
}

// matchConflictSide resolves target (case-insensitive) to a side of block b:
// keepOurs=true when it names the ours label, false when it names theirs. ok is
// false when it matches neither.
func matchConflictSide(b conflictBlock, target string) (keepOurs, ok bool) {
	t := strings.ToLower(strings.TrimSpace(target))
	switch {
	case t == strings.ToLower(b.oursLabel):
		return true, true
	case t == strings.ToLower(b.theirsLabel):
		return false, true
	default:
		return false, false
	}
}

// resolveConflicts replaces each block in blocks with its chosen side's content
// (ours when keepOurs[i], else theirs; the diff3 base section is always
// dropped) and returns the new line slice. Blocks are spliced back-to-front so
// earlier indices stay valid; the input slice is not mutated.
func resolveConflicts(lines []string, blocks []conflictBlock, keepOurs []bool) []string {
	out := lines
	for i := len(blocks) - 1; i >= 0; i-- {
		b := blocks[i]
		var chosen []string
		if keepOurs[i] {
			hi := b.mid
			if b.base != -1 {
				hi = b.base // stop before the diff3 base section
			}
			chosen = out[b.start+1 : hi]
		} else {
			chosen = out[b.mid+1 : b.end]
		}
		next := make([]string, 0, len(out)-(b.end-b.start))
		next = append(next, out[:b.start]...)
		next = append(next, chosen...)
		next = append(next, out[b.end+1:]...)
		out = next
	}
	return out
}
