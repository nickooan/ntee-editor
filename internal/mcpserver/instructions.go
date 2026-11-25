package mcpserver

import "fmt"

// instructions is sent at initialize: it teaches the agent when and how to
// use the edit tools instead of writing files itself, and the show tools to
// put code in front of the user.
const instructions = `ntee stages your file edits in the user's running ntee editor instead of writing them to disk, and shows the user code in that editor.

When an ntee editor is open on the project, read and change files with these tools instead of your own Read/Edit/Write tools or shell reads and writes:
1. read_file before you edit a file: it shows the latest content — edits staged by you or by another agent that are not on disk yet. Your own Read, cat and editors see only the disk. Grep and Glob search the disk too: re-read their matches with read_file before editing them.
2. edit_replace (exact text → new text) or edit_write (a whole file, also for new files), once per change. Edits stack: a later replace works on what earlier edits staged, and editing a file another agent staged builds on their staged edits.
3. edit_end when your part of the change set is complete. Once every agent in the session has sent edit_end, the session finalises: no one may join or edit it but its own agents, and the user is not asked yet.
4. Check it: edit_sandbox returns a scratch copy of every repo the session touches, laid out like the project, with all of its staged edits in place (built once every agent ended, so nobody's half-done change is in it). Run your build and tests there, in any language, then report with edit_check — result pass, fail or skip, and a short note of what you ran and found. The user is asked to review once every agent reported, and sees your note. Never edit files in the sandbox.
5. A check failed and you can fix it: stage the fix with edit_replace/edit_write (the session goes back to editing; every agent checks again), then edit_end again. If you cannot fix it, report fail with a note that says why.
6. Call edit_wait (again while it says still waiting) until it reports the outcome; it also tells you when the session finalises or is back to editing. Builds, tests, linters, Grep and shell commands in the project see the edits only once they are applied.

Rules:
- Paths: relative paths resolve against working_dir — the folder you were launched in, not your shell's current folder. Pass working_dir (absolute) on your first call and again whenever you move to another folder or repo; or pass absolute paths. Moving keeps your edit session: edits in several repos form one change set.
- old_text must match exactly once, whitespace included. On "matches N times", add surrounding lines, or pass line (a line inside the match you mean); replace_all changes every match (renames).
- Sessions are automatic: all edits until edit_end form one session, and the next edit starts a new one. Pass session only to run separate change sets side by side.
- Another agent's session: read_file names the session whose staged edits it shows, and any open session changing the same repo. By default your edits go in your own session and build on theirs. If your change belongs to the same change set, pass that session on your first edit: your later calls (edits, edit_end, edit_check, edit_wait) go there too. edit_end may say it is waiting for other agents — call edit_wait. A session that is finalising refuses newcomers: work in your own session.
- Several agents may edit one file. Edits apply in the order they arrive; each of yours is checked against the version you last read or edited (sent for you). "changed since you read it": another agent changed the file under you — read it again with read_file and stage your change on what is there now. A note saying another agent changed the same lines means: make sure your change fits theirs.
- "not applied … changed on disk since it was staged" (or a session it built on changed or was discarded): re-read it with read_file and stage it again (a new session).
- "ended" or "discarded": the user stopped this change set — do not re-send it unless asked.
- "changes requested" (from edit_wait): the user wants to talk about the session before deciding. Stop calling tools and tell the user, in your reply, that you are ready for their questions or changes. Once they answer, re-read with read_file, stage the follow-ups (they go into the same session), then edit_end and edit_wait again.
- Git after applying: when edit_wait's outcome says the user asks you to commit, commit only the files it lists, on the current branch, with a message you write from the change. When it asks for a pull request, it names the current branch: use it if the user already agreed on it for this change, else ask the user for the branch name (suggest one) and create it; then commit those files, push and open the pull request. When it asks neither, do not touch git.
- The outcome reaches you as a tool result, so your harness may ask the user to confirm the git steps: if it does, or refuses them, say so in your reply and wait for the user. Then call edit_report: done (with commit or pull_request), blocked (it needs the user) or declined, with a note — the user sees it in the editor.
- "set aside for later": the edits stay staged and unwritten until the user decides; carry on, and call edit_wait again when you need the outcome.
- If a tool says no ntee editor is running, nothing was staged: make the edit with your usual file tools.

Showing code (nothing is changed): when the user asks you to show, explain, point out or walk them through code, show it in their editor instead of pasting it:
- One place — a key point, "where is X", "show me the bug": show_code with the file, the line, and a short note on why it matters.
- Several places — "walk me through", "explain how X works", a tour of a change: a walkthrough. Read the code first (read_file, Grep), plan the stops in the order a reader should take them, then send each with walkthrough_step (file, line, note); the editor follows you from stop to stop (auto-drive) and the user can browse back. Call walkthrough_end after the last stop. If the user should finish before you go on, call walkthrough_wait (again while it says still waiting).
- Notes are read in a small box beside the code (8 lines shown, about 100 columns): write a few short, friendly lines — what this code does and why it matters here — one point per stop. Light markdown works: **bold**, ` + "`code`" + `, - bullets. No long paragraphs, logs or big code blocks; more to say goes into another stop.
- Line numbers are those read_file shows. A walkthrough shows each file's diff against HEAD (unchanged code reads as plain context); pass base on the first stop to compare against another revision.
- "the user closed the walkthrough": stop sending stops. The first stop may ask the user to start; carry on sending — the stops queue until they do.`

type toolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

var readSessionProperty = map[string]any{
	"type":        "string",
	"description": "Read as this edit session sees the file (its own staged copy). Leave it out: the automatic session, else the latest content.",
}

var sessionProperty = map[string]any{
	"type":        "string",
	"description": "Edit session id (1–64 letters, digits, '.', '_' or '-'). Leave it out: edits share one automatic session until edit_end. Pass another agent's session (from read_file) on an edit to add your changes to it; later calls then use it too.",
}

var pathProperty = map[string]any{
	"type":        "string",
	"description": "The file, absolute or relative to working_dir (the folder you were launched in until you pass working_dir).",
}

var workingDirProperty = map[string]any{
	"type": "string",
	"description": "Absolute folder you are working in now, e.g. after cd into another repo. Relative paths on this and later calls resolve against it. " +
		"Changing it keeps your edit session: edits in several repos form one change set.",
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	properties["session"] = sessionProperty
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

var notePathProperty = map[string]any{
	"type":        "string",
	"description": "The file to show, absolute or relative to working_dir.",
}

var noteProperty = map[string]any{
	"type":        "string",
	"description": "A few short, friendly lines shown in a box beside the line: what this code does and why it matters. Light markdown (**bold**, `code`, - bullets). Max 16 KiB; keep it far shorter.",
}

// showToolDefinitions are the show tools (show_tools.go): no edit session,
// so no session property.
var showToolDefinitions = []toolDefinition{
	{
		Name:  toolShow,
		Title: "Show the user one place in the code",
		Description: "Open a file at a line in the user's running ntee editor, with an optional note beside it — to point out a key place. Nothing is changed. " +
			"If the user is busy in the editor they are asked first.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        notePathProperty,
				"line":        map[string]any{"type": "integer", "minimum": 1, "description": "The line to show (as read_file numbers it)."},
				"note":        noteProperty,
				"working_dir": workingDirProperty,
			},
			"required":             []string{"path", "line"},
			"additionalProperties": false,
		},
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:  toolWalkthrough,
		Title: "Add a stop to a code walkthrough",
		Description: "Walk the user through code in their ntee editor: each call adds one stop — a file, a line and a note — to a live review the editor follows as you send them. " +
			"The first call starts the walkthrough. Nothing is changed. Send the stops in reading order, then call walkthrough_end.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        notePathProperty,
				"line":        map[string]any{"type": "integer", "minimum": 1, "description": "The line the note sits on (as read_file numbers it). Leave it out: the file's first change."},
				"note":        noteProperty,
				"base":        map[string]any{"type": "string", "description": "First stop only: the revision every file is compared against (default HEAD)."},
				"working_dir": workingDirProperty,
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        toolWalkthroughEnd,
		Title:       "Finish sending the walkthrough",
		Description: "Say the walkthrough has all its stops: the user finishes it at their own pace. Call walkthrough_wait to hear when they are done.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        toolWalkthroughWait,
		Title:       "Wait for the user to finish the walkthrough",
		Description: "Wait until the user finishes or closes the walkthrough (default: the current one, else the one ended last). If it says still waiting, call it again.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": maxWaitSeconds, "description": "How long to wait this call (default 300)."},
			},
			"additionalProperties": false,
		},
		Annotations: map[string]any{"readOnlyHint": true},
	},
}

// toolDefinitions is every tool tools/list offers: the edit tools, then the
// show tools.
var toolDefinitions = append(editToolDefinitions[:len(editToolDefinitions):len(editToolDefinitions)], showToolDefinitions...)

var editToolDefinitions = []toolDefinition{
	{
		Name:  toolReplace,
		Title: "Stage a text replacement",
		Description: "Stage an exact-text replacement in a file, in the running ntee editor (the file on disk is not touched until edit_end applies the session). " +
			"old_text must match exactly once, whitespace included; pass line to pick one of several matches, or replace_all for every match. The reply names the changed lines.",
		InputSchema: objectSchema(map[string]any{
			"path":        pathProperty,
			"working_dir": workingDirProperty,
			"old_text":    map[string]any{"type": "string", "description": "The exact text to find, as staged so far."},
			"new_text":    map[string]any{"type": "string", "description": "Its replacement; empty deletes old_text."},
			"replace_all": map[string]any{"type": "boolean", "description": "Replace every match instead of exactly one."},
			"line":        map[string]any{"type": "integer", "minimum": 1, "description": "When old_text matches several times: a line inside the match to replace."},
		}, "path", "old_text", "new_text"),
	},
	{
		Name:  toolWrite,
		Title: "Stage a whole file",
		Description: "Stage a file's whole new content in the running ntee editor — also how to create a file. " +
			"Nothing is written until edit_end applies the session.",
		InputSchema: objectSchema(map[string]any{
			"path":        pathProperty,
			"working_dir": workingDirProperty,
			"content":     map[string]any{"type": "string", "description": "The complete new file content."},
		}, "path", "content"),
	},
	{
		Name:  toolRead,
		Title: "Read a file's latest content",
		Description: "Read a file through the ntee editor, with line numbers: its latest content, including edits staged by any agent and not written to disk yet (the header says which). " +
			"Your current session reads its own staged copy. Use it instead of your own Read whenever ntee is open.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":        pathProperty,
				"working_dir": workingDirProperty,
				"offset":      map[string]any{"type": "integer", "minimum": 1, "description": "The first line to read (default 1)."},
				"limit":       map[string]any{"type": "integer", "minimum": 1, "description": fmt.Sprintf("How many lines to read (default %d).", defaultReadLines)},
				"session":     readSessionProperty,
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        toolStatus,
		Title:       "List staged files",
		Description: "List the files staged in the current edit session with their +/− line counts and edit counts.",
		InputSchema: objectSchema(map[string]any{}),
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        toolDiscard,
		Title:       "Discard staged edits",
		Description: "Drop every staged edit of the current session; nothing is written. The next edit starts a new session.",
		InputSchema: objectSchema(map[string]any{}),
		Annotations: map[string]any{"destructiveHint": true},
	},
	{
		Name:  toolEnd,
		Title: "Finish the edit session",
		Description: "Finish the session once the change set is complete. Per the user's setting the editor applies every staged file at once (the reply says which were applied), " +
			"or opens them for the user to review — then call edit_wait to hear the outcome. Files changed on disk since they were staged are never overwritten.",
		InputSchema: objectSchema(map[string]any{}),
	},
	{
		Name:  toolSandbox,
		Title: "Build sandbox of the staged edits",
		Description: "Make (or refresh) a scratch copy of every repo the current edit session touches, laid out like the project, with the session's staged files in place — to build, test and search the staged code before the user reviews it. " +
			"The reply names its folder. Never edit files there: stage changes with edit_replace and call this again. Deleted when the session is applied or discarded.",
		InputSchema: objectSchema(map[string]any{
			"fresh":       map[string]any{"type": "boolean", "description": "Rebuild the copy from the disk as it is now (picks up files changed on disk since it was made)."},
			"drop":        map[string]any{"type": "boolean", "description": "Delete the sandbox now."},
			"include":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "More folders the build needs (e.g. a sibling repo), absolute or relative to working_dir."},
			"working_dir": workingDirProperty,
		}),
	},
	{
		Name:  toolCheck,
		Title: "Report your check of the finalising session",
		Description: "After every agent sent edit_end, report what your build and tests in the sandbox (edit_sandbox) found: pass, fail or skip, with a short note. " +
			"The user is asked to review once every agent reported. To fix a failure instead, stage the fix — the session goes back to editing.",
		InputSchema: objectSchema(map[string]any{
			"result": map[string]any{"type": "string", "enum": []string{"pass", "fail", "skip"}, "description": "What the check found."},
			"note":   map[string]any{"type": "string", "description": "What you ran and what it showed, e.g. 'go test ./... ok' or 'npm test: 2 failing (auth.spec)'."},
		}, "result"),
	},
	{
		Name:  toolWait,
		Title: "Wait for the user's review",
		Description: "Wait until the user applies or discards the session they are reviewing (default: the session that ended last). If it says still waiting, call it again. " +
			"If it says changes requested, stop and ask the user in your reply what they want; then stage the follow-ups in the same session and end it again. " +
			"A finished outcome may ask you to commit the applied files, or to open a pull request (it names the current branch; agree the branch with the user). Afterwards call edit_report.",
		InputSchema: objectSchema(map[string]any{
			"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": maxWaitSeconds, "description": "How long to wait this call (default 300)."},
		}),
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:  toolReport,
		Title: "Report the git work the user asked for",
		Description: "After edit_wait's outcome asked you to commit or open a pull request, tell the editor what happened (default: the session that ended last): " +
			"done, blocked (e.g. your harness asked the user to confirm, or refused) or declined. The user sees it in the editor's history.",
		InputSchema: objectSchema(map[string]any{
			"result":       map[string]any{"type": "string", "enum": []string{"done", "blocked", "declined"}, "description": "What happened to the git work."},
			"note":         map[string]any{"type": "string", "description": "A line on what you did or what stopped you, e.g. 'committed on main' or 'git push needs your confirmation in the terminal'."},
			"commit":       map[string]any{"type": "string", "description": "The commit you made (sha), when done."},
			"pull_request": map[string]any{"type": "string", "description": "The pull request URL, when you opened one."},
		}, "result"),
	},
}
