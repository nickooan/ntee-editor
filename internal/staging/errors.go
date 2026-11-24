package staging

import (
	"errors"
	"fmt"
)

// ErrClosedForEdits is wrapped by every refusal of an edit because its
// session takes no more: it is being applied, finished, or the user's. The
// agent starts a new session, which builds on this one's staged edits.
var ErrClosedForEdits = errors.New("start a new session for more edits")

// StaleReadError refuses an edit made on a read the file has changed since,
// where it cannot be made blind to the change: a whole-file write would undo
// it, or a replace overlaps its lines. Front ends add how to read again.
type StaleReadError struct {
	Rel      string
	Change   string // who changed what since the read, and where
	Conflict string // why this edit cannot go on top of it
}

func (e *StaleReadError) Error() string {
	return fmt.Sprintf("%s changed since you read it: %s — %s; read it again and stage your change on the current content", e.Rel, e.Change, e.Conflict)
}

// OldTextMissingError refuses a replace whose old text is not in the file.
type OldTextMissingError struct {
	Rel string
}

func (e *OldTextMissingError) Error() string {
	return fmt.Sprintf("the old text is not in %s — copy it exactly, whitespace included", e.Rel)
}

// OldTextAmbiguousError refuses a replace whose old text matches several
// times, with neither a line to pick one nor all of them asked for.
type OldTextAmbiguousError struct {
	Rel   string
	Count int
	Lines string // the matches' lines, "2, 4"
}

func (e *OldTextAmbiguousError) Error() string {
	return fmt.Sprintf("the old text matches %d times in %s (lines %s) — add surrounding lines to make it unique, name the line it is on, or replace them all", e.Count, e.Rel, e.Lines)
}
