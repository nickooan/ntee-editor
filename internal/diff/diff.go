// Package diff computes a line-level difference between two texts with the
// greedy Myers O(ND) algorithm. It is dependency-free and pure: the app layer
// turns Ops into display rows, this package only decides what changed.
package diff

// OpKind classifies one line of a diff script.
type OpKind uint8

const (
	Equal  OpKind = iota // line present in both; AIdx and BIdx valid
	Delete               // line only in a (old); AIdx valid
	Insert               // line only in b (new); BIdx valid
)

// Op is one line of the edit script from a to b.
type Op struct {
	Kind       OpKind
	AIdx, BIdx int
}

// maxD bounds the Myers search depth. The backtracking trace costs
// O(maxD²) ints (~16MB at 1024), and a real review diff — after common
// prefix/suffix trimming — sits far below it. Past the bound the middle is
// split at lines unique to both sides (anchoredDiff) and each piece diffed
// on its own; only a piece with no such line degrades to "everything
// changed".
const maxD = 1024

// Lines diffs a (old) against b (new), one Op per line, Equal ops included.
// Within every changed region all Deletes precede all Inserts (GitHub hunk
// order), so callers can render removed-then-added blocks directly.
func Lines(a, b []string) []Op {
	// Intern lines so the inner loop compares ints, not strings.
	ids := make(map[string]int, len(a)+len(b))
	ai := internAll(ids, a)
	bi := internAll(ids, b)

	// Trim the common prefix and suffix; only the middle needs Myers.
	n, m := len(ai), len(bi)
	pre := 0
	for pre < n && pre < m && ai[pre] == bi[pre] {
		pre++
	}
	suf := 0
	for suf < n-pre && suf < m-pre && ai[n-1-suf] == bi[m-1-suf] {
		suf++
	}

	ops := make([]Op, 0, n+m-pre-suf)
	for i := 0; i < pre; i++ {
		ops = append(ops, Op{Kind: Equal, AIdx: i, BIdx: i})
	}
	ops = append(ops, myers(ai[pre:n-suf], bi[pre:m-suf], pre, pre)...)
	for i := 0; i < suf; i++ {
		ops = append(ops, Op{Kind: Equal, AIdx: n - suf + i, BIdx: m - suf + i})
	}
	return groupDelsFirst(ops)
}

func internAll(ids map[string]int, lines []string) []int {
	out := make([]int, len(lines))
	for i, s := range lines {
		id, ok := ids[s]
		if !ok {
			id = len(ids)
			ids[s] = id
		}
		out[i] = id
	}
	return out
}

// myers runs the greedy forward Myers algorithm on the trimmed middle and
// backtracks the recorded furthest-reaching frontiers into an edit script.
// aOff/bOff translate local indices back to the untrimmed inputs.
func myers(a, b []int, aOff, bOff int) []Op {
	n, m := len(a), len(b)
	// Fast paths: one side empty would still cost O(len²) in the D-loop.
	if n == 0 {
		out := make([]Op, 0, m)
		for j := 0; j < m; j++ {
			out = append(out, Op{Kind: Insert, BIdx: bOff + j})
		}
		return out
	}
	if m == 0 {
		out := make([]Op, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, Op{Kind: Delete, AIdx: aOff + i})
		}
		return out
	}

	if abs(n-m) > maxD {
		return anchoredDiff(a, b, aOff, bOff) // more than maxD edits for certain
	}
	bound := min(n+m, maxD)
	size := 2*bound + 1
	v := make([]int, size) // furthest x per diagonal k, index k+bound
	var trace [][]int
	found, dFound := false, 0
	for d := 0; d <= bound && !found; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[k-1+bound] < v[k+1+bound]) {
				x = v[k+1+bound] // down: take an Insert
			} else {
				x = v[k-1+bound] + 1 // right: take a Delete
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k+bound] = x
			if x >= n && y >= m {
				found, dFound = true, d
			}
		}
		trace = append(trace, append([]int(nil), v...))
	}
	if !found {
		return anchoredDiff(a, b, aOff, bOff) // bound exceeded: a big rewrite
	}

	// Backtrack from (n, m) through the per-D frontiers, emitting in reverse.
	rev := make([]Op, 0, n+m)
	x, y := n, m
	for d := dFound; d > 0; d-- {
		prev := trace[d-1]
		k := x - y
		var prevK int
		if k == -d || (k != d && prev[k-1+bound] < prev[k+1+bound]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := prev[prevK+bound]
		prevY := prevX - prevK
		for x > prevX && y > prevY { // the snake into this frontier
			rev = append(rev, Op{Kind: Equal, AIdx: aOff + x - 1, BIdx: bOff + y - 1})
			x--
			y--
		}
		if x == prevX {
			rev = append(rev, Op{Kind: Insert, BIdx: bOff + y - 1})
			y--
		} else {
			rev = append(rev, Op{Kind: Delete, AIdx: aOff + x - 1})
			x--
		}
	}
	for x > 0 && y > 0 { // the d=0 leading snake
		rev = append(rev, Op{Kind: Equal, AIdx: aOff + x - 1, BIdx: bOff + y - 1})
		x--
		y--
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// anchoredDiff diffs a middle too different for one bounded Myers search.
// Lines that occur exactly once on each side are matched up, the longest
// run of them in order on both sides becomes the anchors (as patience diff
// does), and the pieces between anchors are diffed one by one — each far
// smaller than the whole. A middle with no such line is all changed.
func anchoredDiff(a, b []int, aOff, bOff int) []Op {
	anchors := uniqueCommonAnchors(a, b)
	if len(anchors) == 0 {
		return allChanged(len(a), len(b), aOff, bOff)
	}
	out := make([]Op, 0, len(a)+len(b))
	aStart, bStart := 0, 0
	for _, anchor := range anchors {
		out = append(out, myers(a[aStart:anchor.a], b[bStart:anchor.b], aOff+aStart, bOff+bStart)...)
		out = append(out, Op{Kind: Equal, AIdx: aOff + anchor.a, BIdx: bOff + anchor.b})
		aStart, bStart = anchor.a+1, anchor.b+1
	}
	return append(out, myers(a[aStart:], b[bStart:], aOff+aStart, bOff+bStart)...)
}

// linePair is a line at a[a] equal to the one at b[b].
type linePair struct{ a, b int }

// uniqueCommonAnchors is the longest sequence of lines unique to both a and
// b whose positions rise on both sides, in order.
func uniqueCommonAnchors(a, b []int) []linePair {
	type seen struct{ inA, inB, atA, atB int }
	counts := map[int]*seen{}
	for index, line := range a {
		entry := counts[line]
		if entry == nil {
			entry = &seen{}
			counts[line] = entry
		}
		entry.inA++
		entry.atA = index
	}
	for index, line := range b {
		if entry := counts[line]; entry != nil {
			entry.inB++
			entry.atB = index
		}
	}
	var pairs []linePair // in a's order
	for index, line := range a {
		if entry := counts[line]; entry.inA == 1 && entry.inB == 1 && entry.atA == index {
			pairs = append(pairs, linePair{a: index, b: entry.atB})
		}
	}
	return longestRisingInB(pairs)
}

// longestRisingInB picks the longest subsequence of pairs (already rising
// in a) whose b also rises — patience sorting, O(k log k).
func longestRisingInB(pairs []linePair) []linePair {
	if len(pairs) == 0 {
		return nil
	}
	tails := []int{}                    // tails[length-1]: index into pairs ending the best run of that length
	previous := make([]int, len(pairs)) // the pair before each one in its run
	for index, pair := range pairs {
		low, high := 0, len(tails)
		for low < high {
			middle := (low + high) / 2
			if pairs[tails[middle]].b < pair.b {
				low = middle + 1
			} else {
				high = middle
			}
		}
		previous[index] = -1
		if low > 0 {
			previous[index] = tails[low-1]
		}
		if low == len(tails) {
			tails = append(tails, index)
		} else {
			tails[low] = index
		}
	}
	run := make([]linePair, len(tails))
	for index, at := len(tails)-1, tails[len(tails)-1]; index >= 0; index, at = index-1, previous[at] {
		run[index] = pairs[at]
	}
	return run
}

func allChanged(n, m, aOff, bOff int) []Op {
	out := make([]Op, 0, n+m)
	for i := 0; i < n; i++ {
		out = append(out, Op{Kind: Delete, AIdx: aOff + i})
	}
	for j := 0; j < m; j++ {
		out = append(out, Op{Kind: Insert, BIdx: bOff + j})
	}
	return out
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// groupDelsFirst reorders each maximal run of non-Equal ops so all Deletes
// precede all Inserts — the removed-block-above-added-block hunk shape.
func groupDelsFirst(ops []Op) []Op {
	out := ops[:0]
	for i := 0; i < len(ops); {
		if ops[i].Kind == Equal {
			out = append(out, ops[i])
			i++
			continue
		}
		j := i
		for j < len(ops) && ops[j].Kind != Equal {
			j++
		}
		run := make([]Op, j-i)
		copy(run, ops[i:j])
		for _, op := range run {
			if op.Kind == Delete {
				out = append(out, op)
			}
		}
		for _, op := range run {
			if op.Kind == Insert {
				out = append(out, op)
			}
		}
		i = j
	}
	return out
}
