package staging

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/nickooan/ntee-editor/internal/diff"
	"github.com/nickooan/ntee-editor/internal/store"
)

// Version names a file content: what an agent read, or what an edit left.
// An edit sent with the version it was made against is checked against the
// content it lands on (Replacement.Expect, WriteExpecting).
func Version(content string) string {
	return store.ContentHash(content)[:12] // the content hash, cut to what a person can quote
}

// maxVersionCacheBytes bounds the contents kept to show what changed since
// an agent's read; past it the oldest go first.
const maxVersionCacheBytes = 64 << 20

// versionCache keeps recent contents by version, so a stale edit can be
// told which lines changed under it. It is memory only: after a restart an
// old version is just "changed".
type versionCache struct {
	mu      sync.Mutex
	entries map[string]string
	order   []string
	bytes   int
}

func newVersionCache() *versionCache {
	return &versionCache{entries: map[string]string{}}
}

// Remember keeps content and returns its version.
func (r *Registry) Remember(content string) string {
	version := Version(content)
	if len(content) > MaxFileBytes {
		return version
	}
	cache := r.versions
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, ok := cache.entries[version]; ok {
		return version
	}
	cache.entries[version] = content
	cache.order = append(cache.order, version)
	cache.bytes += len(content)
	for cache.bytes > maxVersionCacheBytes && len(cache.order) > 1 {
		oldest := cache.order[0]
		cache.order = cache.order[1:]
		cache.bytes -= len(cache.entries[oldest])
		delete(cache.entries, oldest)
	}
	return version
}

func (cache *versionCache) recall(version string) (string, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	content, ok := cache.entries[version]
	return content, ok
}

// lineRange is a 1-based, inclusive run of lines.
type lineRange struct{ from, to int }

func (span lineRange) overlaps(other lineRange) bool {
	return span.from <= other.to && other.from <= span.to
}

func (span lineRange) String() string {
	return lineSpanText(span.from, span.to)
}

func lineSpanText(from, to int) string {
	if to <= from {
		return "L" + strconv.Itoa(from)
	}
	return "L" + strconv.Itoa(from) + "–L" + strconv.Itoa(to)
}

// changedRanges are the lines of current that differ from old; a removal
// marks the line it left off at.
func changedRanges(old, current string) []lineRange {
	var ranges []lineRange
	position := 0 // lines of current passed so far
	mark := func(line int) {
		if count := len(ranges); count > 0 && ranges[count-1].to >= line-1 {
			ranges[count-1].to = max(ranges[count-1].to, line)
			return
		}
		ranges = append(ranges, lineRange{line, line})
	}
	for _, op := range diff.Lines(splitLines(old), splitLines(current)) {
		switch op.Kind {
		case diff.Equal:
			position++
		case diff.Insert:
			position++
			mark(position)
		case diff.Delete:
			mark(max(1, position+1))
		}
	}
	return ranges
}

// staleRead is what changed a file since the version an agent read: the
// lines (when the old content is still known) and who changed them.
type staleRead struct {
	changed []lineRange
	known   bool
	by      string
}

func (r *Registry) staleReadOf(file store.AgentEditFile, expect, current string) staleRead {
	stale := staleRead{}
	if old, ok := r.versions.recall(expect); ok {
		stale.changed, stale.known = changedRanges(old, current), true
	}
	since := -2
	if Version(file.BaseContent) == expect {
		since = -1
	}
	for index, edit := range file.Edits {
		if edit.Version == expect {
			since = index
		}
	}
	switch {
	case since >= -1 && since < len(file.Edits)-1:
		var who []string
		for index, edit := range file.Edits[since+1:] {
			who = append(who, fmt.Sprintf("%s's %s %s (edit %d, %s)", AgentLabel(edit.Agent), edit.Op, lineSpanText(edit.LineFrom, edit.LineTo), since+2+index, edit.At.Local().Format("15:04:05")))
		}
		stale.by = strings.Join(who, ", ")
	case len(file.Edits) == 0 && file.BaseSession != "":
		stale.by = "edit session " + file.BaseSession + " changed its staged copy"
	case len(file.Edits) == 0:
		stale.by = "it changed on disk"
	default:
		stale.by = "another agent or session changed it"
	}
	return stale
}

func (stale staleRead) where() string {
	if !stale.known || len(stale.changed) == 0 {
		return ""
	}
	parts := make([]string, len(stale.changed))
	for index, span := range stale.changed {
		parts[index] = span.String()
	}
	return " — now different at " + strings.Join(parts, ", ")
}

func (stale staleRead) overlaps(span lineRange) bool {
	if !stale.known {
		return true // cannot tell where: assume the worst
	}
	for _, changed := range stale.changed {
		if changed.overlaps(span) {
			return true
		}
	}
	return false
}

// editSpan is the lines an edit replaced, in the content it was applied to.
func editSpan(edit store.AgentEdit) lineRange {
	if edit.All {
		return lineRange{edit.LineFrom, max(edit.LineFrom, edit.LineTo)}
	}
	return lineRange{edit.LineFrom, max(edit.LineFrom, edit.LineFrom+edit.Removed-1)}
}

// othersOnLines names the other agents' earlier edits in the file that
// touched span (in the content after the last edit), each edit's lines
// moved by the edits after it that added or removed lines above them.
func othersOnLines(edits []store.AgentEdit, agent string, span lineRange) []string {
	var touched []string
	for index, edit := range edits {
		if edit.Agent == agent {
			continue
		}
		from, to := edit.LineFrom, max(edit.LineFrom, edit.LineTo)
		for _, later := range edits[index+1:] {
			if later.LineFrom < from {
				shift := later.Added - later.Removed
				from, to = from+shift, to+shift
			}
		}
		if (lineRange{from, to}).overlaps(span) {
			touched = append(touched, fmt.Sprintf("%s (edit %d, %s)", AgentLabel(edit.Agent), index+1, lineSpanText(from, to)))
		}
	}
	return touched
}

// RebaseResult is what replaying a session's edits on a newer copy did.
type RebaseResult struct {
	Session string
	Rebased bool
	Reason  string // why not, when not rebased
}

// rebaseDependents replays, for every session still being edited whose copy
// of rel was built on session's older staged copy, its edits on session's
// new copy — so a change made underneath it does not leave it unappliable.
// A session finalising or ready is left as it is (its checks or review
// saw the old copy); its apply then refuses the file.
func (r *Registry) rebaseDependents(session, rel string) []RebaseResult {
	lower, ok := r.Snapshot(session)
	if !ok {
		return nil
	}
	lowerFile, ok := fileIn([]store.AgentEditSession{lower}, session, rel)
	if !ok {
		return nil
	}
	var results []RebaseResult
	for _, record := range r.Pending() {
		file, ok := fileIn([]store.AgentEditSession{record}, record.Session, rel)
		if !ok || file.BaseSession != session || file.Result != "" || file.BaseContent == lowerFile.Content {
			continue
		}
		if record.Phase() != store.PhaseEditing {
			results = append(results, RebaseResult{Session: record.Session, Reason: "edit session " + record.Session + " is " + string(record.Phase()) + ", so it was left as it was"})
			continue
		}
		results = append(results, r.rebase(record.Session, rel, lowerFile.Content))
	}
	return results
}

func (r *Registry) rebase(session, rel, lowerContent string) RebaseResult {
	result := RebaseResult{Session: session}
	set, ok := r.pendingSet(session)
	if !ok {
		result.Reason = "the session is gone"
		return result
	}
	set.saveMu.Lock()
	defer set.saveMu.Unlock()
	record := set.snapshot()
	files := make([]store.AgentEditFile, len(record.Files))
	copy(files, record.Files)
	index := -1
	for at, file := range files {
		if file.Rel == rel {
			index = at
		}
	}
	if index < 0 {
		result.Reason = "the file is no longer staged there"
		return result
	}
	file := files[index]
	content := lowerContent
	replayed := make([]store.AgentEdit, 0, len(file.Edits))
	for number, edit := range file.Edits {
		if edit.Op != OpReplace {
			result.Reason = fmt.Sprintf("edit %d is a whole-file write, which cannot be replayed on the new copy", number+1)
			return result
		}
		next, again, _, err := replaceText(content, rel, Replacement{Old: edit.Old, New: edit.New, All: edit.All})
		if err != nil {
			result.Reason = fmt.Sprintf("edit %d no longer applies: %v", number+1, err)
			return result
		}
		again.At, again.Agent, again.Version = edit.At, edit.Agent, r.Remember(next)
		replayed = append(replayed, again)
		content = next
	}
	file.BaseContent, file.Content, file.Edits = lowerContent, content, replayed
	files[index] = file
	record.Files = files
	if err := r.log.SavePendingAgentEditFiles(record, []string{rel}); err != nil {
		result.Reason = "cannot save it: " + err.Error()
		return result
	}
	set.publish(record)
	result.Rebased = true
	return result
}

// StaleNote says why the session's copy of rel cannot be written as it is
// (built on a layer that changed or went), "" when it can or is unknown.
func (r *Registry) StaleNote(session, rel string) string {
	record, ok := r.Snapshot(session)
	if !ok {
		return ""
	}
	file, ok := fileIn([]store.AgentEditSession{record}, session, rel)
	if !ok || file.BaseSession == "" || file.Result != "" {
		return ""
	}
	if _, refusal, ok := r.ApplyCheck(file); !ok {
		return refusal.Note
	}
	return ""
}
