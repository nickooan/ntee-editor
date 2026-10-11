package store

import (
	"errors"
	"path/filepath"
	"strings"
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
	if commands[0].FileTypes != AllFileTypes || commands[0].Key() != "build" {
		t.Fatalf("an unset type should be stored as *: %+v key=%q", commands[0], commands[0].Key())
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

	// One name per file-type spec: test for go and rb coexist with tfplan.
	goTest := OpCommand{Name: "test", FileTypes: "go", Command: "go test ./..."}
	rubyTest := OpCommand{Name: "test", FileTypes: "rb", Command: "bundle exec rspec"}
	for _, command := range []OpCommand{rubyTest, goTest} {
		if err := commandStore.PutOpCommand("", command); err != nil {
			t.Fatal(err)
		}
	}
	commands, _ = commandStore.LoadOpCommands()
	if len(commands) != 3 || commands[0].Key() != "test@go" || commands[1].Key() != "test@rb" {
		t.Fatalf("same name per type, sorted by type: %+v", commands)
	}
	// Changing the type moves the entry to its new key.
	if err := commandStore.PutOpCommand(goTest.Key(), OpCommand{Name: "test", FileTypes: "go,sh", Command: "make test"}); err != nil {
		t.Fatal(err)
	}
	if err := commandStore.DeleteOpCommand(rubyTest.Key()); err != nil {
		t.Fatal(err)
	}
	commands, _ = commandStore.LoadOpCommands()
	if len(commands) != 2 || commands[0].Key() != "test@go,sh" || commands[0].Command != "make test" {
		t.Fatalf("type change and delete by key: %+v", commands)
	}
}

func TestOpCommandKey(t *testing.T) {
	cases := []struct {
		command OpCommand
		want    string
	}{
		{OpCommand{Name: "build"}, "build"},
		{OpCommand{Name: "build", FileTypes: "*"}, "build"},
		{OpCommand{Name: "test", FileTypes: "go,rb"}, "test@go,rb"},
	}
	for _, tc := range cases {
		if got := tc.command.Key(); got != tc.want {
			t.Errorf("%+v.Key() = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestGlobalOpCommandsFileTypesDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "global")
	db, err := nteedb.Open(nteedb.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// A record written before file types existed.
	if err := db.Put(opCommandPrefix+"legacy", []byte(`{"name":"legacy","command":"ls"}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	commandStore := NewGlobalOpCommands(dir)
	commands, err := commandStore.LoadOpCommands()
	if err != nil || len(commands) != 1 || commands[0].FileTypes != AllFileTypes || commands[0].Key() != "legacy" {
		t.Fatalf("legacy record should load as *: %+v %v", commands, err)
	}

	if err := commandStore.PutOpCommand("", OpCommand{Name: "fresh", Command: "pwd"}); err != nil {
		t.Fatal(err)
	}
	db, err = nteedb.Open(nteedb.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	data, found, err := db.Get(opCommandPrefix + "fresh")
	if err != nil || !found || !strings.Contains(string(data), `"fileTypes":"*"`) {
		t.Fatalf("an empty type should be written as an explicit *: %s %v %v", data, found, err)
	}
}

func TestGlobalOpCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "global")
	exerciseOpCommands(t, NewGlobalOpCommands(dir))

	// A second handle (another editor instance) sees the same data.
	commands, err := NewGlobalOpCommands(dir).LoadOpCommands()
	if err != nil || len(commands) != 2 || commands[0].Key() != "test@go,sh" || commands[1].Name != "tfplan" {
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
