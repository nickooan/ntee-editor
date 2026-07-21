package filetree

import (
	"os"
	"path"
	"path/filepath"
)

// resolveInsideRoot joins a root-relative slash path to the absolute root,
// rejecting anything that escapes it (.. segments, absolute paths).
func resolveInsideRoot(root, rel string) (string, bool) {
	// Reject absolute paths up front: filepath.Join would silently flatten
	// "/abs" into root/abs, defeating the containment check below.
	if path.IsAbs(rel) || filepath.IsAbs(filepath.FromSlash(rel)) {
		return "", false
	}
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	target := filepath.Join(resolvedRoot, filepath.FromSlash(rel))
	if !isInsideRoot(resolvedRoot, target) || target == resolvedRoot {
		return "", false
	}
	return target, true
}

// MakeDir creates root/rel and any missing parents (mkdir -p). The path must
// stay inside root.
func MakeDir(root, rel string) error {
	target, ok := resolveInsideRoot(root, rel)
	if !ok {
		return os.ErrInvalid
	}
	return os.MkdirAll(target, 0o755)
}

// Remove deletes root/rel — a file, or a directory with everything under it.
// The path must stay inside root; the root itself is never removable.
func Remove(root, rel string) error {
	target, ok := resolveInsideRoot(root, rel)
	if !ok {
		return os.ErrInvalid
	}
	return os.RemoveAll(target)
}

// EnsureFile creates root/rel as an empty file, creating missing parent dirs
// (touch semantics). created is false when the file already exists — that is
// not an error, callers just open it. The path must stay inside root.
func EnsureFile(root, rel string) (created bool, err error) {
	target, ok := resolveInsideRoot(root, rel)
	if !ok {
		return false, os.ErrInvalid
	}
	if _, err := os.Stat(target); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		return false, err
	}
	return true, nil
}
