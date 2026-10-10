package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

// OpCommand is one user-saved shell command template (see internal/opcmd).
type OpCommand struct {
	Name      string    `json:"name"`
	Command   string    `json:"command"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// OpCommandStore persists op-commands globally (shared by every project).
// Implementations are safe for concurrent use; callers run them on tea.Cmd
// goroutines.
type OpCommandStore interface {
	LoadOpCommands() ([]OpCommand, error)
	// PutOpCommand saves command; when previousName is non-empty and differs
	// from command.Name, the old entry is removed (a rename).
	PutOpCommand(previousName string, command OpCommand) error
	DeleteOpCommand(name string) error
}

const opCommandPrefix = "opcmd:"

const (
	globalLockRetries = 5
	globalLockBackoff = 25 * time.Millisecond
)

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
// the db per call and closes it right after rather than holding the flock.
type GlobalOpCommands struct {
	dir string
	mu  sync.Mutex // serializes this process's opens so they never contend for the flock
}

func NewGlobalOpCommands(dir string) *GlobalOpCommands {
	return &GlobalOpCommands{dir: dir}
}

// withDB opens the store, retrying briefly while another instance holds the
// lock for its own short read or write.
func (g *GlobalOpCommands) withDB(run func(db *nteedb.DB) error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := os.MkdirAll(g.dir, 0o755); err != nil {
		return err
	}
	var db *nteedb.DB
	var err error
	for attempt := 0; attempt < globalLockRetries; attempt++ {
		db, err = nteedb.Open(nteedb.Options{Dir: g.dir})
		if !errors.Is(err, nteedb.ErrLocked) {
			break
		}
		time.Sleep(globalLockBackoff * time.Duration(attempt+1))
	}
	if err != nil {
		return err
	}
	runErr := run(db)
	if closeErr := db.Close(); runErr == nil {
		runErr = closeErr
	}
	return runErr
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
				commands = append(commands, command)
			}
		}
		return nil
	})
	sortOpCommands(commands)
	return commands, err
}

func (g *GlobalOpCommands) PutOpCommand(previousName string, command OpCommand) error {
	data, err := json.Marshal(command)
	if err != nil {
		return err
	}
	return g.withDB(func(db *nteedb.DB) error {
		if err := db.Put(opCommandPrefix+command.Name, data); err != nil {
			return err
		}
		if previousName != "" && previousName != command.Name {
			return db.Delete(opCommandPrefix + previousName)
		}
		return nil
	})
}

func (g *GlobalOpCommands) DeleteOpCommand(name string) error {
	return g.withDB(func(db *nteedb.DB) error {
		return db.Delete(opCommandPrefix + name)
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

func (m *MemoryOpCommands) PutOpCommand(previousName string, command OpCommand) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if previousName != "" && previousName != command.Name {
		delete(m.commands, previousName)
	}
	m.commands[command.Name] = command
	return nil
}

func (m *MemoryOpCommands) DeleteOpCommand(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.commands, name)
	return nil
}

func sortOpCommands(commands []OpCommand) {
	sort.Slice(commands, func(a, b int) bool { return commands[a].Name < commands[b].Name })
}
