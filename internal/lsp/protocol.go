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
	ProcessID             int            `json:"processId"`
	RootURI               string         `json:"rootUri"`
	Capabilities          map[string]any `json:"capabilities"`
	InitializationOptions map[string]any `json:"initializationOptions,omitempty"`
}

// clientCapabilities: full-content sync, plain publishDiagnostics, plain
// Location definition responses (no linkSupport → servers send []Location).
var clientCapabilities = map[string]any{
	"textDocument": map[string]any{
		"synchronization":    map[string]any{"didSave": true},
		"publishDiagnostics": map[string]any{},
		"definition":         map[string]any{},
		"references":         map[string]any{},
	},
	"workspace": map[string]any{
		"configuration": true,
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
