package filetree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMakeDirNested(t *testing.T) {
	root := t.TempDir()
	if err := MakeDir(root, "a/b/c"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "a", "b", "c"))
	if err != nil || !info.IsDir() {
		t.Fatalf("nested dir missing: %v", err)
	}
	// Idempotent (mkdir -p).
	if err := MakeDir(root, "a/b/c"); err != nil {
		t.Fatalf("re-creating must not error: %v", err)
	}
}

func TestEnsureFileCreatesParentsAndReportsExisting(t *testing.T) {
	root := t.TempDir()
	created, err := EnsureFile(root, "sub/deep/f.go")
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "deep", "f.go")); err != nil {
		t.Fatal(err)
	}
	created, err = EnsureFile(root, "sub/deep/f.go")
	if err != nil || created {
		t.Fatalf("existing file: created=%v err=%v, want false/nil", created, err)
	}
}

func TestRemoveFileAndDir(t *testing.T) {
	root := t.TempDir()
	if _, err := EnsureFile(root, "d/sub/f.go"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, "d/sub/f.go"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "d", "sub", "f.go")); !os.IsNotExist(err) {
		t.Fatalf("file should be gone: %v", err)
	}
	if err := Remove(root, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "d")); !os.IsNotExist(err) {
		t.Fatalf("dir subtree should be gone: %v", err)
	}
}

func TestCreateRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"../escape", "..", "/abs", ""} {
		if err := MakeDir(root, rel); err == nil {
			t.Errorf("MakeDir(%q) must fail", rel)
		}
		if _, err := EnsureFile(root, rel); err == nil {
			t.Errorf("EnsureFile(%q) must fail", rel)
		}
		if err := Remove(root, rel); err == nil {
			t.Errorf("Remove(%q) must fail — the root itself is never removable", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Fatal("escape path must not exist outside root")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("root itself must survive")
	}
}

func TestCreateRefusesSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "victim.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	// Operating through the escaping link must be refused, existing tail or not.
	if err := MakeDir(root, "link/sub"); err == nil {
		t.Error("MakeDir through an escaping symlink should be refused")
	}
	if _, err := EnsureFile(root, "link/new.txt"); err == nil {
		t.Error("EnsureFile through an escaping symlink should be refused")
	}
	if err := Remove(root, "link/victim.txt"); err == nil {
		t.Error("Remove through an escaping symlink should be refused")
	}
	// Removing the escaping link itself is refused too (resolution escapes).
	if err := Remove(root, "link"); err == nil {
		t.Error("Remove of an escaping symlink should be refused")
	}
	if _, err := os.Stat(filepath.Join(outside, "victim.txt")); err != nil {
		t.Fatal("outside file was touched:", err)
	}
}

func TestCreateAllowsInRootSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureFile(root, "link/a.txt"); err != nil {
		t.Fatal("EnsureFile through an in-root symlink should work:", err)
	}
	if _, err := os.Stat(filepath.Join(root, "real", "a.txt")); err != nil {
		t.Fatal("file should land in the link's target:", err)
	}
	// Removing an in-root link removes the link, not its target.
	if err := Remove(root, "link"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Error("link should be gone")
	}
	if _, err := os.Stat(filepath.Join(root, "real", "a.txt")); err != nil {
		t.Error("link target must survive removing the link:", err)
	}
}
