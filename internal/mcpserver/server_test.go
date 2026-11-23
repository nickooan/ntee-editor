package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickooan/ntee-editor/internal/extmsg"
)

type response struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type callResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError"`
}

// harness runs Serve against a fake editor listening on a real socket. The
// fake records each request and answers with reply.
type harness struct {
	t          *testing.T
	input      *io.PipeWriter
	responses  chan response
	pending    map[string]response
	received   chan string
	served     chan error
	workingDir string
	sessionTag string

	mu       sync.Mutex
	reply    func(request string) string
	noEditor bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	socketDir, err := os.MkdirTemp("/tmp", "nteemcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	h := &harness{
		t:          t,
		responses:  make(chan response, 64),
		pending:    map[string]response{},
		received:   make(chan string, 64),
		served:     make(chan error, 1),
		workingDir: t.TempDir(),
		reply:      func(string) string { return "ok" },
	}
	socketPath := filepath.Join(socketDir, "ntee.sock")
	listener, err := extmsg.Listen(socketPath, func(text string) string {
		h.received <- text
		h.mu.Lock()
		reply := h.reply
		h.mu.Unlock()
		return reply(text)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	now := time.Date(2026, 11, 19, 10, 15, 0, 0, time.Local)
	h.sessionTag = "mcp-20261119-101500-" + strconv.Itoa(os.Getpid()) + "-"
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	h.input = inputWriter
	config := Config{
		FindSocket: func(paths []string) (string, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.noEditor {
				return "", extmsg.ErrNoEditor
			}
			return socketPath, nil
		},
		WorkingDir: h.workingDir,
		Version:    "test",
		Now:        func() time.Time { return now },
	}
	go func() {
		h.served <- Serve(context.Background(), inputReader, outputWriter, config)
		outputWriter.Close()
	}()
	go func() {
		scanner := bufio.NewScanner(outputReader)
		scanner.Buffer(make([]byte, 64<<10), maxLineBytes)
		for scanner.Scan() {
			var decoded response
			if err := json.Unmarshal(scanner.Bytes(), &decoded); err != nil {
				t.Errorf("response is not JSON: %q", scanner.Text())
				continue
			}
			h.responses <- decoded
		}
		close(h.responses)
	}()
	t.Cleanup(func() { inputWriter.Close() })
	return h
}

func (h *harness) setReply(reply func(request string) string) {
	h.mu.Lock()
	h.reply = reply
	h.mu.Unlock()
}

func (h *harness) send(message string) {
	h.t.Helper()
	if _, err := io.WriteString(h.input, message+"\n"); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) request(id int, method string, params any) {
	h.t.Helper()
	message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	data, _ := json.Marshal(message)
	h.send(string(data))
}

// await returns the response to id, keeping others for later awaits.
func (h *harness) await(id string) response {
	h.t.Helper()
	if found, ok := h.pending[id]; ok {
		delete(h.pending, id)
		return found
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case got, open := <-h.responses:
			if !open {
				h.t.Fatalf("output closed before response %s", id)
			}
			if string(got.ID) == id {
				return got
			}
			h.pending[string(got.ID)] = got
		case <-timeout:
			h.t.Fatalf("no response %s", id)
		}
	}
}

func (h *harness) call(id int, tool string, arguments map[string]any) callResult {
	h.t.Helper()
	h.request(id, "tools/call", map[string]any{"name": tool, "arguments": arguments})
	got := h.await(strconv.Itoa(id))
	if got.Error != nil {
		h.t.Fatalf("%s: rpc error %+v", tool, got.Error)
	}
	var result callResult
	if err := json.Unmarshal(got.Result, &result); err != nil || len(result.Content) != 1 {
		h.t.Fatalf("%s: bad result %s", tool, got.Result)
	}
	return result
}

func (h *harness) nextRequest() string {
	h.t.Helper()
	select {
	case text := <-h.received:
		return text
	case <-time.After(5 * time.Second):
		h.t.Fatal("the editor got no request")
		return ""
	}
}

func (h *harness) noRequest() {
	h.t.Helper()
	select {
	case text := <-h.received:
		h.t.Fatalf("the editor got an unexpected request %q", text)
	default:
	}
}

func TestInitializeListAndPing(t *testing.T) {
	h := newHarness(t)
	h.request(1, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "0"}})
	var initialized struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      struct{ Name string }
		Instructions    string `json:"instructions"`
	}
	if err := json.Unmarshal(h.await("1").Result, &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.ProtocolVersion != "2025-03-26" || initialized.ServerInfo.Name != "ntee" || initialized.Capabilities["tools"] == nil {
		t.Fatalf("initialize = %+v", initialized)
	}
	if !strings.Contains(initialized.Instructions, "instead of your own Read/Edit/Write tools") || !strings.Contains(initialized.Instructions, "read_file before you edit") || !strings.Contains(initialized.Instructions, "edit_wait") {
		t.Fatalf("instructions do not teach the flow: %q", initialized.Instructions)
	}

	h.request(2, "initialize", map[string]any{"protocolVersion": "1999-01-01"})
	if err := json.Unmarshal(h.await("2").Result, &initialized); err != nil || initialized.ProtocolVersion != defaultProtocolVersion {
		t.Fatalf("an unknown version should get ours, got %q", initialized.ProtocolVersion)
	}

	h.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	h.request(3, "tools/list", nil)
	var listed struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(h.await("3").Result, &listed); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.InputSchema["type"] != "object" {
			t.Fatalf("%s has no object schema", tool.Name)
		}
	}
	if got := strings.Join(names, ","); got != "edit_replace,edit_write,read_file,edit_status,edit_discard,edit_end,edit_sandbox,edit_check,edit_wait" {
		t.Fatalf("tools = %s", got)
	}

	h.request(4, "ping", nil)
	if got := h.await("4"); got.Error != nil || string(got.Result) != "{}" {
		t.Fatalf("ping = %+v", got)
	}
}

func TestReplaceAndWriteGoOverTheWire(t *testing.T) {
	h := newHarness(t)
	session := h.sessionTag + "1"
	h.setReply(func(string) string { return "ok replaced a.go L12–L14 · edit 1 · 1 file staged" })
	result := h.call(1, toolReplace, map[string]any{"path": "a.go", "old_text": "x\n---\ny", "new_text": "z", "line": 12})
	want, _ := extmsg.EditRequest(extmsg.CommandReplace, session, strings.TrimSuffix(h.sessionTag, "-"), filepath.Join(h.workingDir, "a.go")+"#L12", extmsg.EncodeReplace(extmsg.ReplacePayload{Old: "x\n---\ny", New: "z"}))
	if got := h.nextRequest(); got != want {
		t.Fatalf("replace request = %q, want %q", got, want)
	}
	if result.IsError || result.Content[0].Text != "ok replaced a.go L12–L14 · edit 1 · 1 file staged\nedit session: "+session {
		t.Fatalf("replace result = %+v", result)
	}

	absolute := filepath.Join(t.TempDir(), "new.go")
	h.setReply(func(string) string { return "ok staged new.go (new file, 1 line)" })
	h.call(2, toolWrite, map[string]any{"path": absolute, "content": "package x\n"})
	want, _ = extmsg.EditRequest(extmsg.CommandWrite, session, strings.TrimSuffix(h.sessionTag, "-"), absolute, "package x\n")
	if got := h.nextRequest(); got != want {
		t.Fatalf("write request = %q, want %q", got, want)
	}

	h.call(3, toolReplace, map[string]any{"path": "a.go", "old_text": "a", "new_text": "", "replace_all": true})
	want, _ = extmsg.EditRequest(extmsg.CommandReplace, session, strings.TrimSuffix(h.sessionTag, "-"), filepath.Join(h.workingDir, "a.go"), extmsg.EncodeReplace(extmsg.ReplacePayload{Old: "a", All: true}))
	if got := h.nextRequest(); got != want {
		t.Fatalf("replace-all request = %q, want %q", got, want)
	}
}

func TestReadFileNumbersTheLatestContentAndSaysWhereItCameFrom(t *testing.T) {
	h := newHarness(t)
	h.setReply(func(string) string { return extmsg.BodyReplyWith("one\n\n  three\n", "disk") })
	result := h.call(1, toolRead, map[string]any{"path": "a.go"})
	if result.IsError || result.Content[0].Text != "a.go — on disk, no staged edits\n     1\tone\n     2\t\n     3\t  three" {
		t.Fatalf("read = %q", result.Content[0].Text)
	}
	if got := h.nextRequest(); got != "read\n"+filepath.Join(h.workingDir, "a.go")+"\n" {
		t.Fatalf("without a session a read names none (and starts none): %q", got)
	}

	h.setReply(func(string) string {
		return extmsg.BodyReplyWith("a\nb\nc\nd\n", "staged · edit session other-agent · not written")
	})
	result = h.call(2, toolRead, map[string]any{"path": "a.go", "offset": 2, "limit": 2})
	if want := "a.go — latest staged content from edit session other-agent, NOT on disk yet (your edits build on it; to add them to that change set instead, pass session=other-agent)\n     2\tb\n     3\tc\n(lines 2–3 of 4 — pass offset 4 to read on)"; result.Content[0].Text != want {
		t.Fatalf("offset/limit read = %q", result.Content[0].Text)
	}
	h.nextRequest()
	if result = h.call(3, toolRead, map[string]any{"path": "a.go", "offset": 9}); !strings.Contains(result.Content[0].Text, "offset 9 is past the end: the file has 4 lines") {
		t.Fatalf("past the end = %q", result.Content[0].Text)
	}
	h.nextRequest()

	h.setReply(func(string) string { return "ok staged a.go" })
	h.call(4, toolWrite, map[string]any{"path": "a.go", "content": "x"})
	h.nextRequest()
	own := h.sessionTag + "1"
	h.setReply(func(string) string {
		return extmsg.BodyReplyWith("x\n", "staged · edit session "+own+" · not written")
	})
	if result = h.call(5, toolRead, map[string]any{"path": "a.go"}); result.Content[0].Text != "a.go — your edit session "+own+"'s staged copy, NOT on disk yet\n     1\tx" {
		t.Fatalf("own copy = %q", result.Content[0].Text)
	}
	if got := h.nextRequest(); got != "read --edit-session="+own+"\n"+filepath.Join(h.workingDir, "a.go")+"\n" {
		t.Fatalf("a read goes as the current session: %q", got)
	}

	h.setReply(func(string) string { return "error no such file: b.go — not staged and not on disk" })
	if result = h.call(6, toolRead, map[string]any{"path": "b.go"}); !result.IsError || result.Content[0].Text != "error no such file: b.go — not staged and not on disk" {
		t.Fatalf("a refusal comes back as is: %+v", result)
	}
	h.nextRequest()
	h.setReply(func(string) string { return extmsg.BodyReply("a.go\tmodified · +1 −0 · 1 edit\n") })
	if result := h.call(7, toolStatus, nil); result.Content[0].Text != "a.go\tmodified · +1 −0 · 1 edit\n\nedit session: "+own {
		t.Fatalf("status = %q", result.Content[0].Text)
	}
}

func TestReadTextCutsVeryLongLines(t *testing.T) {
	text := readText(toolArguments{Path: "a.go"}, "", "disk", strings.Repeat("é", maxReadLineRunes+5)+"\n")
	if !strings.HasSuffix(text, "… [line cut at 2000 characters]") || strings.Count(text, "é") != maxReadLineRunes {
		t.Fatalf("long line = %q…", text[:40])
	}
	if text := readText(toolArguments{Path: "a.go"}, "", "disk", ""); text != "a.go — on disk, no staged edits\n(empty file)" {
		t.Fatalf("empty = %q", text)
	}
}

func TestSessionsAreAutomatic(t *testing.T) {
	h := newHarness(t)
	first, second := h.sessionTag+"1", h.sessionTag+"2"
	agent := " --agent=" + strings.TrimSuffix(h.sessionTag, "-")
	if result := h.call(1, toolStatus, nil); !result.IsError || !strings.Contains(result.Content[0].Text, "no edit session yet") {
		t.Fatalf("status before any edit = %+v", result)
	}
	h.noRequest()

	h.call(2, toolWrite, map[string]any{"path": "a.go", "content": "x"})
	h.call(3, toolReplace, map[string]any{"path": "a.go", "old_text": "x", "new_text": "y"})
	for range 2 {
		if got := h.nextRequest(); !strings.Contains(got, "--edit-session="+first+agent+"\n") {
			t.Fatalf("edit went to %q, want session %s", got, first)
		}
	}

	h.setReply(func(string) string {
		return "ok edit session " + first + " ended — the user is reviewing 1 staged file; to hear the outcome run: ntee --msg --msg-edit-session " + first + " wait"
	})
	result := h.call(4, toolEnd, nil)
	if got := h.nextRequest(); got != "end --edit-session="+first+agent+"\n" {
		t.Fatalf("end request = %q", got)
	}
	if text := result.Content[0].Text; !strings.Contains(text, "to hear the outcome call edit_wait") || strings.Contains(text, "ntee --msg") {
		t.Fatalf("end reply should point at edit_wait: %q", text)
	}

	h.setReply(func(string) string { return "ok finished — edit session " + first + " applied 1 file" })
	h.call(5, toolWait, nil)
	if got := h.nextRequest(); got != extmsg.EditWaitRequest(first, 5*time.Minute) {
		t.Fatalf("wait request = %q, want the ended session with a 5m budget", got)
	}

	h.setReply(func(string) string { return "ok" })
	h.call(6, toolWrite, map[string]any{"path": "a.go", "content": "x"})
	if got := h.nextRequest(); !strings.Contains(got, "--edit-session="+second+agent+"\n") {
		t.Fatalf("the edit after end went to %q, want a new session %s", got, second)
	}
	h.call(7, toolWrite, map[string]any{"path": "b.go", "content": "x", "session": "side-task"})
	if got := h.nextRequest(); !strings.Contains(got, "--edit-session=side-task"+agent+"\n") {
		t.Fatalf("an explicit session is used as given, got %q", got)
	}
	h.call(8, toolDiscard, nil)
	if got := h.nextRequest(); got != "discard --edit-session="+second+"\n" {
		t.Fatalf("discard request = %q, want the automatic session", got)
	}
	if result := h.call(9, toolStatus, nil); !result.IsError {
		t.Fatalf("after discard there is no current session, got %+v", result)
	}
}

func TestAnExplicitSessionJoinsWhenNoneIsCurrent(t *testing.T) {
	h := newHarness(t)
	agent := strings.TrimSuffix(h.sessionTag, "-")
	h.call(1, toolReplace, map[string]any{"path": "a.go", "old_text": "x", "new_text": "y", "session": "their-session"})
	if got := h.nextRequest(); !strings.HasPrefix(got, "replace --edit-session=their-session --agent="+agent+"\n") {
		t.Fatalf("the edit joins their session as this agent: %q", got)
	}
	h.call(2, toolWrite, map[string]any{"path": "b.go", "content": "x"})
	if got := h.nextRequest(); !strings.HasPrefix(got, "write --edit-session=their-session --agent=") {
		t.Fatalf("later edits without a session stay in the joined one: %q", got)
	}
	h.setReply(func(string) string {
		return "ok your part of edit session their-session is done — waiting for 1 other agent (agent-a) to end before the user reviews it; to hear the outcome run: ntee --msg --msg-edit-session their-session wait"
	})
	result := h.call(3, toolEnd, nil)
	if got := h.nextRequest(); got != "end --edit-session=their-session --agent="+agent+"\n" {
		t.Fatalf("end goes to the joined session: %q", got)
	}
	if text := result.Content[0].Text; result.IsError || !strings.Contains(text, "waiting for 1 other agent") || !strings.Contains(text, "call edit_wait") {
		t.Fatalf("a partial end says to wait: %+v", result)
	}
	h.setReply(func(string) string { return "ok finished — edit session their-session applied 2 files" })
	h.call(4, toolWait, nil)
	if got := h.nextRequest(); got != extmsg.EditWaitRequest("their-session", 5*time.Minute) {
		t.Fatalf("wait follows the joined session: %q", got)
	}
}

func TestEditSandboxAsksForTheCurrentSessionsBox(t *testing.T) {
	h := newHarness(t)
	if result := h.call(1, toolSandbox, nil); !result.IsError || !strings.Contains(result.Content[0].Text, "no edit session yet") {
		t.Fatalf("a sandbox never starts a session: %+v", result)
	}
	h.noRequest()
	h.call(2, toolWrite, map[string]any{"path": "a.go", "content": "x"})
	h.nextRequest()
	session := h.sessionTag + "1"
	h.setReply(func(string) string {
		return "ok sandbox /box/" + session + " · holds the project · 1 staged file in place"
	})
	result := h.call(3, toolSandbox, nil)
	if got := h.nextRequest(); got != extmsg.SandboxRequest(session, "", nil) {
		t.Fatalf("request = %q", got)
	}
	if result.IsError || !strings.HasPrefix(result.Content[0].Text, "ok sandbox /box/"+session) {
		t.Fatalf("reply = %+v", result)
	}
	h.call(4, toolSandbox, map[string]any{"fresh": true, "include": []string{"../libs/core"}})
	if got, want := h.nextRequest(), extmsg.SandboxRequest(session, extmsg.SandboxFresh, []string{filepath.Join(h.workingDir, "../libs/core")}); got != want {
		t.Fatalf("fresh with include = %q, want %q", got, want)
	}
	h.call(5, toolSandbox, map[string]any{"drop": true})
	if got := h.nextRequest(); got != extmsg.SandboxRequest(session, extmsg.SandboxDrop, nil) {
		t.Fatalf("drop = %q", got)
	}
	if result := h.call(6, toolSandbox, map[string]any{"drop": true, "fresh": true}); !result.IsError {
		t.Fatal("drop and fresh together are refused")
	}
}

func TestReadFileNamesOpenSessionsInTheRepo(t *testing.T) {
	text := readText(toolArguments{Path: "a.go"}, "", "disk · also editing ntee-editor: S (agent-a, agent-b)", "x\n")
	if !strings.HasPrefix(text, "a.go — on disk, no staged edits\nopen edit sessions changing ntee-editor: S (agent-a, agent-b) — if your change belongs with one of theirs, pass session=<its id>") {
		t.Fatalf("header = %q", text)
	}
}

func TestFinalisingChecksAndFixesFollowTheEndedSession(t *testing.T) {
	h := newHarness(t)
	agent := strings.TrimSuffix(h.sessionTag, "-")
	session := h.sessionTag + "1"
	h.call(1, toolWrite, map[string]any{"path": "a.go", "content": "x"})
	h.nextRequest()
	h.setReply(func(string) string {
		return "ok edit session " + session + " is finalising — every agent's edits are in and no one else may join; build and test them in its sandbox (run: ntee --msg --msg-edit-session " + session +
			" sandbox), then report with check pass, fail or skip (what you ran in a note); the user reviews once every agent checked — to hear the outcome run: ntee --msg --msg-edit-session " + session + " wait"
	})
	result := h.call(2, toolEnd, nil)
	h.nextRequest()
	if text := result.Content[0].Text; !strings.Contains(text, "(call edit_sandbox)") || !strings.Contains(text, "report with edit_check (pass, fail or skip)") || !strings.Contains(text, "call edit_wait") {
		t.Fatalf("the finalising reply speaks in tools: %q", text)
	}

	h.setReply(func(string) string { return "ok sandbox /box · holds the project · 1 staged file in place" })
	h.call(3, toolSandbox, nil)
	if got := h.nextRequest(); got != extmsg.SandboxRequest(session, "", nil) {
		t.Fatalf("the sandbox is the ended session's: %q", got)
	}
	h.setReply(func(string) string {
		return "ok replaced a.go L1 · edit 2 · 1 file staged · edit session " + session + " is back to editing"
	})
	h.call(4, toolReplace, map[string]any{"path": "a.go", "old_text": "x", "new_text": "y"})
	if got := h.nextRequest(); !strings.HasPrefix(got, "replace --edit-session="+session+" ") {
		t.Fatalf("a fix after edit_end goes to the same session: %q", got)
	}
	h.setReply(func(string) string { return "ok edit session " + session + " is finalising — …" })
	h.call(5, toolEnd, nil)
	h.nextRequest()
	h.setReply(func(string) string {
		return "ok edit session " + session + " ended — the user is reviewing 1 staged file; to hear the outcome call edit_wait"
	})
	h.call(6, toolCheck, map[string]any{"result": "pass", "note": "go test ./... ok"})
	if got, want := h.nextRequest(), extmsg.CheckRequest(session, agent, extmsg.CheckPass, "go test ./... ok"); got != want {
		t.Fatalf("check = %q, want %q", got, want)
	}
	if result := h.call(7, toolCheck, map[string]any{"result": "maybe"}); !result.IsError {
		t.Fatal("a check needs pass, fail or skip")
	}

	// The session is the user's now: the next edit is refused there, and
	// goes to a new session instead.
	h.setReply(func(request string) string {
		if strings.Contains(request, "--edit-session="+session+" ") {
			return "error edit session " + session + " is ready for the user's review — start a new session for more edits"
		}
		return "ok staged b.go"
	})
	h.call(8, toolWrite, map[string]any{"path": "b.go", "content": "z"})
	if got := h.nextRequest(); !strings.HasPrefix(got, "write --edit-session="+h.sessionTag+"2 ") {
		t.Fatalf("an edit after the user took over starts a new session: %q", got)
	}
}

func TestWaitPassesOnFinalisingAndReopening(t *testing.T) {
	h := newHarness(t)
	h.call(1, toolWrite, map[string]any{"path": "a.go", "content": "x"})
	h.nextRequest()
	h.call(2, toolEnd, nil)
	h.nextRequest()
	h.setReply(func(string) string {
		return "ok waiting — edit session S is finalising: every agent ended; build and test it in its sandbox (sandbox), report with check, then wait again for the user's decision"
	})
	result := h.call(3, toolWait, nil)
	h.nextRequest()
	if text := result.Content[0].Text; result.IsError || !strings.Contains(text, "its sandbox (edit_sandbox), report with edit_check,") {
		t.Fatalf("a finalising wait says what to do, in tools: %+v", result)
	}
}

func TestEditsCarryTheVersionTheAgentLastSaw(t *testing.T) {
	h := newHarness(t)
	h.setReply(func(string) string { return extmsg.BodyReplyWith("x\n", "disk · version 0123456789ab") })
	h.call(1, toolRead, map[string]any{"path": "a.go"})
	h.nextRequest()
	h.setReply(func(string) string { return "ok replaced a.go L1 · edit 1 · 1 file staged · version abcdefabcdef" })
	h.call(2, toolReplace, map[string]any{"path": "a.go", "old_text": "x", "new_text": "y"})
	if got := h.nextRequest(); !strings.Contains(strings.SplitN(got, "\n", 2)[0], " --expect=0123456789ab") {
		t.Fatalf("the edit names the version read: %q", got)
	}
	h.call(3, toolWrite, map[string]any{"path": "a.go", "content": "z"})
	if got := h.nextRequest(); !strings.Contains(strings.SplitN(got, "\n", 2)[0], " --expect=abcdefabcdef") {
		t.Fatalf("the next edit names the version the last one left: %q", got)
	}
	h.call(4, toolWrite, map[string]any{"path": "b.go", "content": "z"})
	if got := h.nextRequest(); strings.Contains(got, "--expect=") {
		t.Fatalf("a file never seen is edited unchecked: %q", got)
	}
	h.setReply(func(string) string { return "ok discarded edit session x — 2 staged files dropped, nothing written" })
	h.call(5, toolDiscard, nil)
	h.nextRequest()
	h.setReply(func(string) string { return "ok staged a.go" })
	h.call(6, toolWrite, map[string]any{"path": "a.go", "content": "again"})
	if got := h.nextRequest(); strings.Contains(got, "--expect=") {
		t.Fatalf("once the session is gone, so are the versions of its copies: %q", got)
	}
}

func TestReadFileWarnsAboutAStaleCopy(t *testing.T) {
	text := readText(toolArguments{Path: "a.go"}, "S", "staged · edit session S · not written · version 0123456789ab · stale: edit session A changed it after this session built on it", "x\n")
	if !strings.Contains(text, "⚠ your copy can no longer be applied as it is: edit session A changed it after this session built on it") {
		t.Fatalf("header = %q", text)
	}
}

func TestEditorRepliesAndRefusals(t *testing.T) {
	h := newHarness(t)
	h.setReply(func(string) string {
		return "error the old text matches 2 times in a.go (lines 3, 9) — add surrounding lines to make it unique, add #L<n> to the path, or replace them all with --msg-replace-all"
	})
	result := h.call(1, toolReplace, map[string]any{"path": "a.go", "old_text": "x", "new_text": "y"})
	if text := result.Content[0].Text; !result.IsError || !strings.Contains(text, "pass line") || !strings.Contains(text, "with replace_all") || strings.Contains(text, "--msg") {
		t.Fatalf("an error reply should be isError in tool terms: %+v", result)
	}
	h.nextRequest()

	refusals := []struct {
		tool      string
		arguments map[string]any
		want      string
	}{
		{toolReplace, map[string]any{"path": "a.go", "new_text": "y"}, "needs old_text"},
		{toolReplace, map[string]any{"path": "a.go", "old_text": "x"}, "needs new_text"},
		{toolReplace, map[string]any{"path": "a.go", "old_text": "x", "new_text": "y", "line": 3, "replace_all": true}, "leave it off with replace_all"},
		{toolWrite, map[string]any{"content": "x"}, "needs path"},
		{toolWrite, map[string]any{"path": "a.go"}, "needs content"},
		{toolRead, map[string]any{}, "needs path"},
		{toolWait, map[string]any{"timeout_seconds": 7200}, "at most 3600"},
		{toolWrite, map[string]any{"path": 5}, "bad arguments"},
	}
	for index, refusal := range refusals {
		result := h.call(10+index, refusal.tool, refusal.arguments)
		if !result.IsError || !strings.Contains(result.Content[0].Text, refusal.want) {
			t.Fatalf("%s %v = %+v, want an error with %q", refusal.tool, refusal.arguments, result, refusal.want)
		}
	}
	h.noRequest()

	h.mu.Lock()
	h.noEditor = true
	h.mu.Unlock()
	result = h.call(30, toolWrite, map[string]any{"path": "a.go", "content": "x"})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "make the edit with your usual file tools") {
		t.Fatalf("no editor = %+v", result)
	}
}

func TestProtocolErrors(t *testing.T) {
	h := newHarness(t)
	h.send("not json")
	if got := h.await("null"); got.Error == nil || got.Error.Code != codeParseError {
		t.Fatalf("bad JSON = %+v", got)
	}
	h.request(1, "resources/list", nil)
	if got := h.await("1"); got.Error == nil || got.Error.Code != codeMethodNotFound {
		t.Fatalf("unknown method = %+v", got)
	}
	h.request(2, "tools/call", map[string]any{"arguments": map[string]any{}})
	if got := h.await("2"); got.Error == nil || got.Error.Code != codeInvalidParams {
		t.Fatalf("call without a name = %+v", got)
	}
	h.request(3, "tools/call", map[string]any{"name": "rm_rf"})
	if got := h.await("3"); got.Error == nil || got.Error.Code != codeInvalidParams {
		t.Fatalf("unknown tool = %+v", got)
	}
	h.send(`{"id":4,"method":"ping"}`)
	if got := h.await("4"); got.Error == nil || got.Error.Code != codeInvalidRequest {
		t.Fatalf("no jsonrpc version = %+v", got)
	}
}

func TestWaitTellsTheAgentToCallAgain(t *testing.T) {
	h := newHarness(t)
	h.setReply(func(string) string {
		return "ok waiting — the user hasn't answered yet; run: ntee --msg --msg-edit-session e1 wait"
	})
	result := h.call(1, toolWait, map[string]any{"session": "e1", "timeout_seconds": 30})
	if result.IsError || result.Content[0].Text != "still waiting — the user hasn't finished reviewing edit session e1; call edit_wait again" {
		t.Fatalf("wait = %+v", result)
	}
	if got := h.nextRequest(); got != extmsg.EditWaitRequest("e1", 30*time.Second) {
		t.Fatalf("wait request = %q", got)
	}
}

func TestWaitSaysWhenTheUserSetTheEditsAside(t *testing.T) {
	h := newHarness(t)
	h.setReply(func(string) string {
		return "ok waiting — the user set edit session e1 aside for later: still staged, nothing written; wait again to hear their decision"
	})
	result := h.call(1, toolWait, map[string]any{"session": "e1"})
	if text := result.Content[0].Text; result.IsError || !strings.Contains(text, "set edit session e1 aside for later") || !strings.Contains(text, "call edit_wait") {
		t.Fatalf("deferred wait = %+v", result)
	}
}

func TestAnEditorThatHangsUpIsReportedNotSilent(t *testing.T) {
	h := newHarness(t)
	h.setReply(func(string) string { return "" }) // the editor quit while holding the wait
	result := h.call(1, toolWait, map[string]any{"session": "e1"})
	if text := result.Content[0].Text; !result.IsError || !strings.Contains(text, "closed the connection without answering") || !strings.Contains(text, "edit_status") {
		t.Fatalf("a hang-up = %+v", result)
	}
}

func TestACancelledWaitStopsWithoutAResponseWhileOthersAnswer(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.setReply(func(request string) string {
		if strings.HasPrefix(request, extmsg.CommandWait) {
			<-release
		}
		return extmsg.BodyReply("a.go\n")
	})
	h.request(1, "tools/call", map[string]any{"name": toolWait, "arguments": map[string]any{"session": "e1"}})
	if got := h.nextRequest(); !strings.HasPrefix(got, "wait ") {
		t.Fatalf("first request = %q", got)
	}
	if result := h.call(2, toolStatus, map[string]any{"session": "e1"}); result.IsError {
		t.Fatalf("status during a wait = %+v", result)
	}
	h.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"user stopped"}}`)
	h.request(3, "ping", nil)
	h.await("3")

	h.input.Close()
	select {
	case err := <-h.served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return: the cancelled wait is still running")
	}
	for got := range h.responses {
		if string(got.ID) == "1" {
			t.Fatalf("a cancelled call got a response: %s", got.Result)
		}
	}
	if _, ok := h.pending["1"]; ok {
		t.Fatal("a cancelled call got a response")
	}
}

func TestCallsRunningWhenInputEndsStillAnswer(t *testing.T) {
	h := newHarness(t)
	h.setReply(func(string) string {
		time.Sleep(100 * time.Millisecond)
		return "ok staged a.go"
	})
	h.request(1, "tools/call", map[string]any{"name": toolWrite, "arguments": map[string]any{"path": "a.go", "content": "x"}})
	h.input.Close()
	if got := h.await("1"); got.Error != nil || !strings.Contains(string(got.Result), "ok staged a.go") {
		t.Fatalf("the call should still answer: %+v", got)
	}
	if err := <-h.served; err != nil {
		t.Fatal(err)
	}
}

func TestToolTermsReplaceCLIHints(t *testing.T) {
	cases := map[string]string{
		"ok edit session e1 ended — the user is reviewing 2 staged files; to hear the outcome run: ntee --msg --msg-edit-session e1 wait": "ok edit session e1 ended — the user is reviewing 2 staged files; to hear the outcome call edit_wait",
		"error the old text is not in a.go — copy it exactly, whitespace included (ntee --msg show prints the staged content)":            "error the old text is not in a.go — copy it exactly, whitespace included (read_file shows the staged content)",
		"ok replaced a.go L1–L2 · edit 1 · 1 file staged":                                                                                 "ok replaced a.go L1–L2 · edit 1 · 1 file staged",
	}
	for reply, want := range cases {
		if got := forTools(reply); got != want {
			t.Fatalf("forTools(%q) = %q, want %q", reply, got, want)
		}
	}
}

func TestAChangeRequestSendsTheNextEditBackToTheSession(t *testing.T) {
	h := newHarness(t)
	session := h.sessionTag + "1"
	h.call(1, toolWrite, map[string]any{"path": "a.go", "content": "x"})
	h.nextRequest()
	h.setReply(func(string) string {
		return "ok edit session " + session + " ended — the user is reviewing 1 staged file; to hear the outcome run: ntee --msg --msg-edit-session " + session + " wait"
	})
	h.call(2, toolEnd, nil)
	h.nextRequest()

	h.setReply(func(string) string {
		return extmsg.ChangesRequestedReply("the user has questions or changes for edit session " + session + ": stop and tell them to switch to this terminal")
	})
	result := h.call(3, toolWait, nil)
	if got := h.nextRequest(); !strings.HasPrefix(got, "wait --edit-session="+session+" ") {
		t.Fatalf("the wait is the ended session's: %q", got)
	}
	if text := result.Content[0].Text; result.IsError || !strings.Contains(text, "changes requested") || !strings.Contains(text, "do not call edit_wait again yet") {
		t.Fatalf("the agent is told to ask the user: %+v", result)
	}

	h.setReply(func(string) string { return "ok replaced a.go L1 · edit 2 · 1 file staged" })
	h.call(4, toolReplace, map[string]any{"path": "a.go", "old_text": "x", "new_text": "y"})
	if got := h.nextRequest(); !strings.HasPrefix(got, "replace --edit-session="+session+" ") {
		t.Fatalf("the follow-up goes to the same session, open for edits again: %q", got)
	}
}

func TestAFinishedOutcomeCarriesTheUsersGitFollowUp(t *testing.T) {
	h := newHarness(t)
	h.setReply(func(string) string {
		return "ok finished — edit session e1 applied 1 file · the user asks for a new branch and a pull request: first ask them for the branch name (suggest one from the change); then create the branch, commit only a.go, push it and open the pull request"
	})
	result := h.call(1, toolWait, map[string]any{"session": "e1"})
	if text := result.Content[0].Text; result.IsError || !strings.Contains(text, "first ask them for the branch name") || !strings.Contains(text, "commit only a.go") {
		t.Fatalf("the follow-up reaches the agent intact: %+v", result)
	}
}
