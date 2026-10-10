package store

import (
	"errors"
	"path/filepath"
	"testing"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

func exerciseOpCommands(t *testing.T, commandStore OpCommandStore) {
	t.Helper()
	if commands, err := commandStore.LoadOpCommands(); err != nil || len(commands) != 0 {
		t.Fatalf("empty store: %v %v", commands, err)
	}
	for _, command := range []OpCommand{{Name: "plan", Command: "terraform plan {$1}"}, {Name: "build", Command: "make"}} {
		if err := commandStore.PutOpCommand("", command); err != nil {
			t.Fatal(err)
		}
	}
	commands, err := commandStore.LoadOpCommands()
	if err != nil || len(commands) != 2 || commands[0].Name != "build" || commands[1].Command != "terraform plan {$1}" {
		t.Fatalf("load sorted by name: %+v %v", commands, err)
	}

	if err := commandStore.PutOpCommand("plan", OpCommand{Name: "tfplan", Command: "terraform plan"}); err != nil {
		t.Fatal(err)
	}
	commands, _ = commandStore.LoadOpCommands()
	if len(commands) != 2 || commands[1].Name != "tfplan" || commands[1].Command != "terraform plan" {
		t.Fatalf("rename should replace the old entry: %+v", commands)
	}

	if err := commandStore.DeleteOpCommand("build"); err != nil {
		t.Fatal(err)
	}
	commands, _ = commandStore.LoadOpCommands()
	if len(commands) != 1 || commands[0].Name != "tfplan" {
		t.Fatalf("after delete: %+v", commands)
	}
}

func TestGlobalOpCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "global")
	exerciseOpCommands(t, NewGlobalOpCommands(dir))

	// A second handle (another editor instance) sees the same data.
	commands, err := NewGlobalOpCommands(dir).LoadOpCommands()
	if err != nil || len(commands) != 1 || commands[0].Name != "tfplan" {
		t.Fatalf("second handle: %+v %v", commands, err)
	}
}

func TestMemoryOpCommands(t *testing.T) {
	exerciseOpCommands(t, NewMemoryOpCommands())
}

func TestGlobalOpCommandsLockedReturnsError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "global")
	commandStore := NewGlobalOpCommands(dir)
	if err := commandStore.PutOpCommand("", OpCommand{Name: "a", Command: "ls"}); err != nil {
		t.Fatal(err)
	}
	holder, err := nteedb.Open(nteedb.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commandStore.LoadOpCommands(); !errors.Is(err, nteedb.ErrLocked) {
		t.Fatalf("held lock: err = %v, want ErrLocked", err)
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	if commands, err := commandStore.LoadOpCommands(); err != nil || len(commands) != 1 {
		t.Fatalf("after release: %+v %v", commands, err)
	}
}
