package filetree

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// containedPath joins rel (a root-relative path; "" = the root itself) to
// root and verifies containment both lexically and after symlink resolution —
// a symlink inside the root pointing outside it is an escape. real is the
// symlink-resolved target (the not-yet-existing tail, if any, rides along
// verbatim); lexical is the plain cleaned join, suitable for display and for
// operations that must act on a link rather than its target. ok is false on
// any escape or resolution failure.
func containedPath(root, rel string) (lexical, real string, ok bool) {
	if root == "" {
		return "", "", false
	}
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", false
	}
	lexical = filepath.Join(resolvedRoot, filepath.FromSlash(rel))
	if !isInsideRoot(resolvedRoot, lexical) {
		return "", "", false
	}
	// The root itself may be a symlink (darwin's /tmp → /private/tmp), so
	// containment must be re-checked in fully-resolved space.
	realRoot, err := filepath.EvalSymlinks(resolvedRoot)
	if err != nil {
		return "", "", false
	}
	// Resolve the deepest existing ancestor of the target, carrying the
	// missing tail along verbatim (the lexical join above is cleaned, so the
	// tail contains no ".." segments).
	p, suffix := lexical, ""
	for {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			if !isInsideRoot(realRoot, resolved) {
				return "", "", false
			}
			return lexical, filepath.Join(resolved, suffix), true
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", false
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", "", false
		}
		suffix = filepath.Join(filepath.Base(p), suffix)
		p = parent
	}
}

// resolveInsideRoot is containedPath plus the stricter create/remove rules:
// absolute rels are rejected up front and the root itself is never a valid
// target. The returned path is the lexical join — Remove on a symlink must
// delete the link, not its target — but its resolution is verified to stay
// inside the root, so a link pointing outside the root is refused.
func resolveInsideRoot(root, rel string) (string, bool) {
	// Reject absolute paths explicitly: filepath.Join would silently flatten
	// "/abs" into root/abs, masking the caller's mistake.
	if path.IsAbs(rel) || filepath.IsAbs(filepath.FromSlash(rel)) {
		return "", false
	}
	lexical, _, ok := containedPath(root, rel)
	if !ok {
		return "", false
	}
	if absRoot, err := filepath.Abs(root); err != nil || lexical == absRoot {
		return "", false
	}
	return lexical, true
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
// The path must stay inside root; the root itself is never removable, and a
// symlink whose target resolves outside the root is refused rather than
// removed (its resolution escapes the jail).
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
