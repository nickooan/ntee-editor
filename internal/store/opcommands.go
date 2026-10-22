package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

// OpCommand is one user-saved shell command template (see internal/opcmd).
// FileTypes is the normalized TYPE spec ("go,rb", or "*" for every file);
// records saved before the field existed load as "*".
type OpCommand struct {
	Name      string    `json:"name"`
	FileTypes string    `json:"fileTypes"`
	Command   string    `json:"command"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// AllFileTypes is the FileTypes value matching every file (opcmd.AllFileTypes).
const AllFileTypes = "*"

// Key is the command's storage identity. One name may exist once per file-type
// spec, so a typed command is keyed "name@types"; a "*" command keeps the bare
// name, which is exactly the key records had before file types existed.
func (command OpCommand) Key() string {
	if command.FileTypes == "" || command.FileTypes == AllFileTypes {
		return command.Name
	}
	return command.Name + "@" + command.FileTypes
}

func (command OpCommand) withDefaults() OpCommand {
	if command.FileTypes == "" {
		command.FileTypes = AllFileTypes
	}
	return command
}

// OpCommandStore persists op-commands globally (shared by every project).
// Implementations are safe for concurrent use; callers run them on tea.Cmd
// goroutines.
type OpCommandStore interface {
	LoadOpCommands() ([]OpCommand, error)
	// PutOpCommand saves command under command.Key(); when previousKey is
	// non-empty and differs, the old entry is removed (a rename or a TYPE
	// change).
	PutOpCommand(previousKey string, command OpCommand) error
	DeleteOpCommand(key string) error
}

const opCommandPrefix = "opcmd:"

// GlobalDir is the store directory shared by every project.
func GlobalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ntee-editor", "global"), nil
}

// GlobalOpCommands is the ntee-db-backed OpCommandStore. Every editor instance
// shares this one store and ntee-db allows a single writer process, so it opens
// the db per call (withDBAt) and closes it right after rather than holding the
// flock.
type GlobalOpCommands struct {
	dir string
}

func NewGlobalOpCommands(dir string) *GlobalOpCommands {
	return &GlobalOpCommands{dir: dir}
}

func (g *GlobalOpCommands) withDB(run func(db *nteedb.DB) error) error {
	return withDBAt(nteedb.Options{Dir: g.dir}, run)
}

func (g *GlobalOpCommands) LoadOpCommands() ([]OpCommand, error) {
	var commands []OpCommand
	err := g.withDB(func(db *nteedb.DB) error {
		keys, err := db.PrefixScan(opCommandPrefix)
		if err != nil || len(keys) == 0 {
			return err
		}
		values, found, err := db.GetMany(keys)
		if err != nil {
			return err
		}
		for index := range keys {
			var command OpCommand
			if found[index] && json.Unmarshal(values[index], &command) == nil {
				commands = append(commands, command.withDefaults())
			}
		}
		return nil
	})
	sortOpCommands(commands)
	return commands, err
}

func (g *GlobalOpCommands) PutOpCommand(previousKey string, command OpCommand) error {
	command = command.withDefaults()
	data, err := json.Marshal(command)
	if err != nil {
		return err
	}
	key := command.Key()
	return g.withDB(func(db *nteedb.DB) error {
		if err := db.Put(opCommandPrefix+key, data); err != nil {
			return err
		}
		if previousKey != "" && previousKey != key {
			return db.Delete(opCommandPrefix + previousKey)
		}
		return nil
	})
}

func (g *GlobalOpCommands) DeleteOpCommand(key string) error {
	return g.withDB(func(db *nteedb.DB) error {
		return db.Delete(opCommandPrefix + key)
	})
}

// MemoryOpCommands is the in-process OpCommandStore (tests, and the fallback
// when the global store's directory can't be resolved).
type MemoryOpCommands struct {
	mu       sync.Mutex
	commands map[string]OpCommand
}

func NewMemoryOpCommands() *MemoryOpCommands {
	return &MemoryOpCommands{commands: map[string]OpCommand{}}
}

func (m *MemoryOpCommands) LoadOpCommands() ([]OpCommand, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	commands := make([]OpCommand, 0, len(m.commands))
	for _, command := range m.commands {
		commands = append(commands, command)
	}
	sortOpCommands(commands)
	return commands, nil
}

func (m *MemoryOpCommands) PutOpCommand(previousKey string, command OpCommand) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	command = command.withDefaults()
	key := command.Key()
	if previousKey != "" && previousKey != key {
		delete(m.commands, previousKey)
	}
	m.commands[key] = command
	return nil
}

func (m *MemoryOpCommands) DeleteOpCommand(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.commands, key)
	return nil
}

// sortOpCommands orders by name, then file types with "*" first.
func sortOpCommands(commands []OpCommand) {
	sort.Slice(commands, func(a, b int) bool {
		if commands[a].Name != commands[b].Name {
			return commands[a].Name < commands[b].Name
		}
		if (commands[a].FileTypes == AllFileTypes) != (commands[b].FileTypes == AllFileTypes) {
			return commands[a].FileTypes == AllFileTypes
		}
		return commands[a].FileTypes < commands[b].FileTypes
	})
}
