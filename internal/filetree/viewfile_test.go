package filetree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListDirFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.txt", "a.txt", filepath.Join("sub", "c.txt")} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	names, ok := ListDirFiles(root, "")
	if !ok {
		t.Fatal("root listing must succeed")
	}
	if len(names) != 2 || names[0] != "a.txt" || names[1] != "b.txt" {
		t.Fatalf("names = %v (directories must be excluded, order sorted)", names)
	}

	names, ok = ListDirFiles(root, "sub")
	if !ok || len(names) != 1 || names[0] != "c.txt" {
		t.Fatalf("sub names = %v ok=%v", names, ok)
	}

	if _, ok := ListDirFiles(root, ".."); ok {
		t.Fatal("paths escaping the root must be rejected")
	}
	if _, ok := ListDirFiles(root, "missing"); ok {
		t.Fatal("an unreadable directory must report !ok")
	}
}

func TestReadViewFileRefusesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadViewFile(root, "link/secret.txt"); ok {
		t.Error("reading through an escaping symlink should be refused")
	}
	if _, ok := ListDirFiles(root, "link"); ok {
		t.Error("listing through an escaping symlink should be refused")
	}
}

func TestWriteViewFileJailAndAtomicity(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteViewFile(root, "a.txt", "new"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new" {
		t.Fatalf("content = %q, %v", data, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 preserved", info.Mode().Perm())
	}
	// No temp-file droppings left behind.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ntee-save-") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}

	// Escapes are refused.
	if err := WriteViewFile(root, "../evil.txt", "x"); err == nil {
		t.Error("write outside the root should be refused")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "victim.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := WriteViewFile(root, "link/victim.txt", "pwn"); err == nil {
		t.Error("write through an escaping symlink should be refused")
	}
	if data, _ := os.ReadFile(filepath.Join(outside, "victim.txt")); string(data) != "keep" {
		t.Errorf("outside file was modified: %q", data)
	}
	// The root itself is not writable.
	if err := WriteViewFile(root, ".", "x"); err == nil {
		t.Error("writing the root itself should be refused")
	}
}

func TestWriteViewFileFollowsInRootSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real.txt")
	if err := os.WriteFile(real, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := WriteViewFile(root, "link.txt", "new"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(real); string(data) != "new" {
		t.Errorf("resolved file content = %q, want new", data)
	}
	if info, err := os.Lstat(filepath.Join(root, "link.txt")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("the link itself should survive a save")
	}
}
