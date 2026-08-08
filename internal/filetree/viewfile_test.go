package filetree

import (
	"os"
	"path/filepath"
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
