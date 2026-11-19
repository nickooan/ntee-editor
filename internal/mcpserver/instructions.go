package mcpserver

// instructions is sent at initialize: it teaches the agent when and how to
// use the edit tools instead of writing files itself.
const instructions = `ntee stages your file edits in the user's running ntee editor instead of writing them to disk.

When an ntee editor is open on the project, make every file change with these tools instead of your own Edit/Write tools or shell writes:
1. edit_replace (exact text → new text) or edit_write (a whole file, also for new files), once per change. Edits stack: a later replace works on what earlier edits staged.
2. edit_show prints a file as staged — your own file reads still see the disk.
3. edit_end when the change set is complete. Depending on the user's setting the editor either applies everything at once, or opens it for the user to review.
4. If edit_end says the user is reviewing, call edit_wait (again while it says still waiting) until it reports the outcome.
5. Builds, tests and linters see your edits only once they are applied: run them after edit_end (and edit_wait) reports them applied.

Rules:
- old_text must match exactly once, whitespace included. On "matches N times", add surrounding lines, or pass line (a line inside the match you mean); replace_all changes every match (renames).
- Sessions are automatic: all edits until edit_end form one session, and the next edit starts a new one. Pass session only to run separate change sets side by side.
- "not applied … changed on disk since it was staged": that file changed under you — re-read it and stage it again (a new session).
- "ended" or "discarded": the user stopped this change set — do not re-send it unless asked.
- If a tool says no ntee editor is running, nothing was staged: make the edit with your usual file tools.`

type toolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

var sessionProperty = map[string]any{
	"type":        "string",
	"description": "Edit session id (1–64 letters, digits, '.', '_' or '-'). Leave it out: edits share one automatic session until edit_end.",
}

var pathProperty = map[string]any{
	"type":        "string",
	"description": "The file, relative to the project folder or absolute.",
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	properties["session"] = sessionProperty
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

var toolDefinitions = []toolDefinition{
	{
		Name:  toolReplace,
		Title: "Stage a text replacement",
		Description: "Stage an exact-text replacement in a file, in the running ntee editor (the file on disk is not touched until edit_end applies the session). " +
			"old_text must match exactly once, whitespace included; pass line to pick one of several matches, or replace_all for every match. The reply names the changed lines.",
		InputSchema: objectSchema(map[string]any{
			"path":        pathProperty,
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
			"path":    pathProperty,
			"content": map[string]any{"type": "string", "description": "The complete new file content."},
		}, "path", "content"),
	},
	{
		Name:        toolShow,
		Title:       "Show a staged file",
		Description: "Print a file as staged in the current edit session (or as on disk when the session has not touched it). Use it to check staged edits — your own file reads see the disk.",
		InputSchema: objectSchema(map[string]any{"path": pathProperty}, "path"),
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
		Name:        toolWait,
		Title:       "Wait for the user's review",
		Description: "Wait until the user applies or discards the session they are reviewing (default: the session that ended last). If it says still waiting, call it again.",
		InputSchema: objectSchema(map[string]any{
			"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": maxWaitSeconds, "description": "How long to wait this call (default 300)."},
		}),
		Annotations: map[string]any{"readOnlyHint": true},
	},
}
