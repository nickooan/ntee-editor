package app

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/store"
)

// externalMessageMsg delivers one accepted external request to the UI.
// reply, when set, takes the UI's immediate outcome back to the sender
// ("opened", "waiting for the user"); replay marks a re-run from the history
// view, which must not touch the stored record.
type externalMessageMsg struct {
	record store.ExternalRecord
	replay bool
	reply  chan<- string
}

// externalReplyWait bounds how long a sender waits for the UI's outcome
// before getting a plain "delivered" — a busy UI must not stall the sender.
const externalReplyWait = 3 * time.Second

// externalQueueSize bounds requests waiting for the UI; beyond it senders are
// told the editor is busy instead of piling up goroutines.
const externalQueueSize = 32

// maxStoredRawBytes caps the raw text kept in history (the paths are kept in
// full; the raw copy is only for display).
const maxStoredRawBytes = 8 << 10

// ExternalReceiver is the socket server's handler: it checks the global
// switch, validates the request against the project's file jail, records it
// in the project history, and hands it to the UI. It runs on the server's
// connection goroutines, never the UI's.
type ExternalReceiver struct {
	workspaceRoot string
	settings      store.ExternalSettingsStore
	history       store.ExternalHistoryStore
	queue         chan externalMessageMsg
}

func NewExternalReceiver(workspaceRoot string, settings store.ExternalSettingsStore, history store.ExternalHistoryStore) *ExternalReceiver {
	return &ExternalReceiver{
		workspaceRoot: workspaceRoot,
		settings:      settings,
		history:       history,
		queue:         make(chan externalMessageMsg, externalQueueSize),
	}
}

// SetSink starts delivering queued requests to the UI (program.Send). One
// dispatcher goroutine keeps them in arrival order; requests that arrived
// before the sink was set wait in the queue.
func (r *ExternalReceiver) SetSink(sink func(msg any)) {
	go func() {
		for message := range r.queue {
			sink(message)
		}
	}()
}

// Handle is the extmsg.Handler.
func (r *ExternalReceiver) Handle(text string) string {
	settings, err := r.settings.LoadExternalSettings()
	if err != nil {
		return extmsg.ErrorReply("cannot read the external-message settings: " + err.Error())
	}
	if !settings.Enabled {
		return extmsg.ErrorReply("external messages are disabled — enable them in ntee under Ctrl+T › external")
	}
	message, err := extmsg.Parse(text)
	if err != nil {
		return extmsg.ErrorReply(err.Error())
	}
	paths := make([]string, len(message.Paths))
	for i, raw := range message.Paths {
		rel, err := r.resolvePath(raw, message.Command == extmsg.CommandOpen)
		if err != nil {
			return extmsg.ErrorReply(err.Error())
		}
		paths[i] = rel
	}

	now := time.Now()
	raw := text
	if len(raw) > maxStoredRawBytes {
		raw = raw[:maxStoredRawBytes] + "…"
	}
	record := store.ExternalRecord{
		Key:        store.NewExternalRecordKey(now),
		Command:    message.Command,
		Args:       message.Args,
		Base:       cmp.Or(message.Base(), store.ExternalDefaultBase),
		Paths:      paths,
		Raw:        raw,
		ReceivedAt: now,
		Status:     store.ExternalPending,
	}
	historyWarning := ""
	if err := r.history.AppendExternal(record, settings.HistoryLimit); err != nil {
		historyWarning = " (not saved to history: " + err.Error() + ")"
	}

	reply := make(chan string, 1)
	select {
	case r.queue <- externalMessageMsg{record: record, reply: reply}:
	default:
		return extmsg.ErrorReply("the editor is busy — try again shortly")
	}
	select {
	case outcome := <-reply:
		return replyFromOutcome(outcome) + historyWarning
	case <-time.After(externalReplyWait):
		return extmsg.OkReply("delivered" + historyWarning)
	}
}

// replyFromOutcome turns the UI's outcome into the sender's reply line: an
// outcome starting with "error " is already an error reply.
func replyFromOutcome(outcome string) string {
	if extmsg.IsErrorReply(outcome) {
		return outcome
	}
	return extmsg.OkReply(outcome)
}

// relativeToWorkspace makes an absolute path workspace-relative. The sender
// and the editor may spell the same directory differently (macOS /var vs
// /private/var), so a lexical miss retries with symlinks resolved. A path
// outside the workspace comes back starting with "..".
func (r *ExternalReceiver) relativeToWorkspace(absolute string) string {
	if rel, err := filepath.Rel(r.workspaceRoot, absolute); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	realRoot, err := filepath.EvalSymlinks(r.workspaceRoot)
	if err != nil {
		return ".."
	}
	// A deleted file has no real path; resolve its deepest existing ancestor.
	existing, missing := absolute, ""
	for {
		if resolved, err := filepath.EvalSymlinks(existing); err == nil {
			if rel, err := filepath.Rel(realRoot, filepath.Join(resolved, missing)); err == nil {
				return rel
			}
			return ".."
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return ".."
		}
		missing = filepath.Join(filepath.Base(existing), missing)
		existing = parent
	}
}

// resolvePath maps a sender's path — absolute, or relative to the opened
// folder, optionally with a #L<n> anchor — to a workspace-relative path with
// the anchor kept. It must stay inside the workspace's file jail. An open
// target must be an existing file; review paths may be deleted files.
func (r *ExternalReceiver) resolvePath(raw string, mustExist bool) (string, error) {
	pathText, line := splitLineAnchor(raw)
	if pathText == "" {
		return "", fmt.Errorf("empty path in %q", raw)
	}
	rel := filepath.FromSlash(pathText)
	if filepath.IsAbs(rel) {
		rel = r.relativeToWorkspace(rel)
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || !filetree.IsContained(r.workspaceRoot, rel) {
		return "", fmt.Errorf("path outside the project: %s", pathText)
	}
	if mustExist {
		info, err := os.Stat(filepath.Join(r.workspaceRoot, filepath.FromSlash(rel)))
		switch {
		case errors.Is(err, os.ErrNotExist):
			return "", fmt.Errorf("no such file: %s", pathText)
		case err != nil:
			return "", err
		case info.IsDir():
			return "", fmt.Errorf("not a file: %s", pathText)
		}
	}
	if line > 0 {
		rel += fmt.Sprintf("#L%d", line)
	}
	return rel, nil
}
