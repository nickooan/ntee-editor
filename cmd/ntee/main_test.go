package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickooan/ntee-editor/internal/app"
	"github.com/nickooan/ntee-editor/internal/store"
)

func TestResolveStartTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		argument, root, file string
		line                 int
		wantErr              string
	}{
		{argument: dir, root: dir},
		{argument: filepath.Join(dir, ".env"), root: dir, file: ".env"},
		{argument: filepath.Join(dir, "a.go") + "#L12", root: dir, file: "a.go", line: 12},
		{argument: filepath.Join(dir, "new.txt"), root: dir, file: "new.txt"},
		{argument: filepath.Join(dir, "missing", "new.txt"), wantErr: "no such file or directory"},
		{argument: dir + "#L3", wantErr: "needs a file"},
	}
	for _, c := range cases {
		root, file, line, err := resolveStartTarget(c.argument)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want %q", c.argument, err, c.wantErr)
			}
			continue
		}
		if err != nil || root != c.root || file != c.file || line != c.line {
			t.Errorf("%s = (%q, %q, %d, %v), want (%q, %q, %d)", c.argument, root, file, line, err, c.root, c.file, c.line)
		}
	}
}

func TestSingleFileModeNeverStartsTheSocket(t *testing.T) {
	// A short HOME keeps the socket path under the platform's sun_path limit.
	home, err := os.MkdirTemp("/tmp", "nteehome")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	project := t.TempDir()

	server, socketPath, problem := listenForExternalMessages(project, nil)
	if server != nil || !strings.Contains(problem, "single-file mode") {
		t.Fatalf("single-file mode: server=%v problem=%q", server, problem)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("no socket file may exist in single-file mode: %v", err)
	}

	// A folder does listen, on the same path.
	receiver := app.NewExternalReceiver(project, store.NewMemoryExternalSettings(store.DefaultExternalSettings), store.NewMemoryExternalHistory())
	server, folderSocket, problem := listenForExternalMessages(project, receiver)
	if server == nil || problem != "" || folderSocket != socketPath {
		t.Fatalf("folder mode: server=%v problem=%q", server, problem)
	}
	defer server.Close()
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("a folder editor listens: %v", err)
	}
}
