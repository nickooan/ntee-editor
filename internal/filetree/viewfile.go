package filetree

import (
	"os"
	"path/filepath"
)

// OpenViewFile is a file loaded for viewing/editing.
type OpenViewFile struct {
	FileName string
	Path     string // absolute path
	Content  string
	// Binary is true when the file looks like a native/binary file (not safe to
	// display as text). Content is left empty in that case.
	Binary bool
}

// looksBinary reports whether data appears to be a binary (non-text) file. A NUL
// byte in the leading window is the classic, cheap heuristic used by editors.
func looksBinary(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// ReadViewFile reads a file under root for viewing/editing, jailed to the root.
// Read errors surface as the file content. ok is false when the path escapes
// the root or is empty.
func ReadViewFile(root, relativePath string) (OpenViewFile, bool) {
	if root == "" || relativePath == "" {
		return OpenViewFile{}, false
	}

	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return OpenViewFile{}, false
	}
	resolvedPath := filepath.Join(resolvedRoot, relativePath)
	if !isInsideRoot(resolvedRoot, resolvedPath) {
		return OpenViewFile{}, false
	}

	name := filepath.Base(relativePath)
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return OpenViewFile{FileName: name, Path: resolvedPath, Content: err.Error()}, true
	}
	if looksBinary(data) {
		return OpenViewFile{FileName: name, Path: resolvedPath, Binary: true}, true
	}
	return OpenViewFile{FileName: name, Path: resolvedPath, Content: string(data)}, true
}

// WriteViewFile writes content to an already-opened file path.
func WriteViewFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
