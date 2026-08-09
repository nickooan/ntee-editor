package filetree

import (
	"io/fs"
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

// ReadViewFile reads a file under root for viewing/editing, jailed to the
// root (symlink-aware: an in-root link pointing outside the root is refused).
// Read errors surface as the file content. ok is false when the path escapes
// the root or is empty.
func ReadViewFile(root, relativePath string) (OpenViewFile, bool) {
	if root == "" || relativePath == "" {
		return OpenViewFile{}, false
	}
	lexical, real, ok := containedPath(root, relativePath)
	if !ok {
		return OpenViewFile{}, false
	}

	name := filepath.Base(relativePath)
	data, err := os.ReadFile(real)
	if err != nil {
		return OpenViewFile{FileName: name, Path: lexical, Content: err.Error()}, true
	}
	if looksBinary(data) {
		return OpenViewFile{FileName: name, Path: lexical, Binary: true}, true
	}
	return OpenViewFile{FileName: name, Path: lexical, Content: string(data)}, true
}

// WriteViewFile atomically writes content to root/rel, jailed like
// ReadViewFile: the content lands in a temp file in the target's directory
// which is then renamed over the target, so a crash mid-save cannot leave a
// truncated file. An existing file's permissions are preserved (new files get
// 0o644); saving through an in-root symlink replaces the resolved file and
// keeps the link itself intact.
func WriteViewFile(root, rel, content string) error {
	if rel == "" {
		return os.ErrInvalid
	}
	lexical, target, ok := containedPath(root, rel)
	if !ok {
		return os.ErrInvalid
	}
	if absRoot, err := filepath.Abs(root); err != nil || lexical == absRoot {
		return os.ErrInvalid
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".ntee-save-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// ListDirFiles lists the non-directory entry names in one directory under
// root ("" = the root itself), jailed like ReadViewFile. Names come back in
// os.ReadDir's sorted order. ok is false when the path escapes the root or
// cannot be read.
func ListDirFiles(root, dirRel string) ([]string, bool) {
	if root == "" {
		return nil, false
	}
	_, resolvedDir, ok := containedPath(root, dirRel)
	if !ok {
		return nil, false
	}
	entries, err := os.ReadDir(resolvedDir)
	if err != nil {
		return nil, false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, true
}
