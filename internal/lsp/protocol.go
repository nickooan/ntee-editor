package lsp

import (
	"encoding/json"
	"net/url"
)

// Minimal typed structs for the LSP subset this editor speaks, plus the two
// offset bridges: file paths ↔ file:// URIs, and rune columns ↔ UTF-16 code
// units (LSP positions are UTF-16).

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type contentChange struct {
	Text string `json:"text"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []contentChange                 `json:"contentChanges"`
}

type didSaveParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type definitionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type completionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// CompletionItem is the subset of an LSP completion candidate the editor uses.
// InsertText falls back to Label when empty; FilterText falls back to Label.
type CompletionItem struct {
	Label      string `json:"label"`
	Kind       int    `json:"kind"`
	Detail     string `json:"detail"`
	InsertText string `json:"insertText"`
	SortText   string `json:"sortText"`
	FilterText string `json:"filterText"`
}

type completionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

type referenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

type referenceParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      referenceContext       `json:"context"`
}

type protoDiagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Message  string `json:"message"`
}

type publishDiagnosticsParams struct {
	URI         string            `json:"uri"`
	Diagnostics []protoDiagnostic `json:"diagnostics"`
}

type initializeParams struct {
	ProcessID             int               `json:"processId"`
	RootURI               string            `json:"rootUri"`
	WorkspaceFolders      []workspaceFolder `json:"workspaceFolders,omitempty"`
	Capabilities          map[string]any    `json:"capabilities"`
	InitializationOptions map[string]any    `json:"initializationOptions,omitempty"`
}

// workspaceFolder is one root the server should treat as a project. Scoping the
// server to the file's own repo (rather than a huge multi-repo root) keeps its
// initialize/index fast.
type workspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type workspaceFoldersChangeEvent struct {
	Added   []workspaceFolder `json:"added"`
	Removed []workspaceFolder `json:"removed"`
}

type didChangeWorkspaceFoldersParams struct {
	Event workspaceFoldersChangeEvent `json:"event"`
}

// executeCommandParams is a workspace/executeCommand request — used by the
// hybrid bridge to relay a Vue tsserver command to the TypeScript server.
type executeCommandParams struct {
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// clientCapabilities: full-content sync, plain publishDiagnostics, plain
// Location definition responses (no linkSupport → servers send []Location).
var clientCapabilities = map[string]any{
	"textDocument": map[string]any{
		"synchronization":    map[string]any{"didSave": true},
		"publishDiagnostics": map[string]any{},
		"definition":         map[string]any{},
		"references":         map[string]any{},
		"completion":         map[string]any{},
	},
	"workspace": map[string]any{
		"configuration":    true,
		"workspaceFolders": true,
	},
}

// PathToURI converts an absolute file path to a file:// URI.
func PathToURI(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	return u.String()
}

// URIToPath converts a file:// URI back to a path; ok is false for other
// schemes or unparseable URIs.
func URIToPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	return u.Path, true
}

// UTF16Col converts a rune column in line to UTF-16 code units.
func UTF16Col(line string, runeCol int) int {
	col, i := 0, 0
	for _, r := range line {
		if i >= runeCol {
			break
		}
		if r > 0xFFFF {
			col += 2
		} else {
			col++
		}
		i++
	}
	return col
}

// RuneCol converts a UTF-16 code-unit column in line to a rune column.
func RuneCol(line string, utf16Col int) int {
	units, i := 0, 0
	for _, r := range line {
		if units >= utf16Col {
			break
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
		i++
	}
	return i
}

// parseCompletion accepts either a CompletionList ({isIncomplete, items}) or a
// bare []CompletionItem, or null.
func parseCompletion(raw json.RawMessage) []CompletionItem {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list completionList
	if err := json.Unmarshal(raw, &list); err == nil && list.Items != nil {
		return list.Items
	}
	var items []CompletionItem
	if err := json.Unmarshal(raw, &items); err == nil {
		return items
	}
	return nil
}

// parseLocations accepts the three shapes a definition response can take:
// Location, []Location, or []LocationLink.
func parseLocations(raw json.RawMessage) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var many []Location
	if err := json.Unmarshal(raw, &many); err == nil && len(many) > 0 && many[0].URI != "" {
		return many
	}
	var one Location
	if err := json.Unmarshal(raw, &one); err == nil && one.URI != "" {
		return []Location{one}
	}
	var links []struct {
		TargetURI            string `json:"targetUri"`
		TargetSelectionRange Range  `json:"targetSelectionRange"`
	}
	if err := json.Unmarshal(raw, &links); err == nil {
		out := make([]Location, 0, len(links))
		for _, l := range links {
			if l.TargetURI != "" {
				out = append(out, Location{URI: l.TargetURI, Range: l.TargetSelectionRange})
			}
		}
		return out
	}
	return nil
}
