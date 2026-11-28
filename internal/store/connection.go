package store

import (
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

const (
	globalLockRetries = 5
	globalLockBackoff = 25 * time.Millisecond

	defaultConnectionIdleClose = 30 * time.Second
)

// ConnectionMode is how a db's connection lives.
type ConnectionMode string

const (
	// ConnectionSessionLong is opened at start and closed at quit.
	ConnectionSessionLong ConnectionMode = "session-long"
	// ConnectionSessionLease stays open while agent sessions use it, and
	// closes when the last one ends or after it sat idle.
	ConnectionSessionLease ConnectionMode = "session lease"
	// ConnectionPerCall opens and closes around every call.
	ConnectionPerCall ConnectionMode = "per call"
)

// ConnectionStatus is a snapshot of one db connection, for the Ctrl+T pane.
type ConnectionStatus struct {
	Name       string
	Mode       ConnectionMode
	Open       bool
	Sessions   int
	Opens      int
	Calls      int
	Reconnects int
	OpenedAt   time.Time
	LastUsed   time.Time
	ClosesAt   time.Time // zero unless an open lease will close on its own
	LastError  string
	Note       string
}

// ConnectionReporter is a store that can describe its db connections.
// Connections never waits on a db call in flight.
type ConnectionReporter interface {
	Connections() []ConnectionStatus
}

// connectionUse is how one call uses its directory's connection.
type connectionUse struct {
	lease bool   // keep the connection open after the call
	begin string // a session that starts using the connection
	end   string // a session done with it: the last one closes it
}

// dbConnection is the one ntee-db connection this process has to a
// directory. Every store over the directory goes through it, so two of them
// never contend for its single-writer flock.
type dbConnection struct {
	dir       string
	idleClose time.Duration

	mu       sync.Mutex
	options  nteedb.Options
	db       *nteedb.DB
	leased   bool
	active   map[string]bool
	lastUsed time.Time
	idle     *time.Timer

	// statsMu is never held across a db call, so a snapshot never waits for
	// one (a compaction can hold mu for a long while).
	statsMu sync.Mutex
	stats   ConnectionStatus
}

var connections sync.Map // dir → *dbConnection

func connectionAt(dir string) *dbConnection {
	if found, ok := connections.Load(dir); ok {
		return found.(*dbConnection)
	}
	created, _ := connections.LoadOrStore(dir, &dbConnection{dir: dir, idleClose: defaultConnectionIdleClose, active: map[string]bool{}})
	return created.(*dbConnection)
}

// withDBAt runs one call on the db at options.Dir, opened for the call and
// closed right after unless a lease holds it open. options.Indexes must be
// the same on every call for a directory — an index left out is
// soft-dropped.
func withDBAt(options nteedb.Options, run func(db *nteedb.DB) error) error {
	return connectionAt(options.Dir).with(options, connectionUse{}, run)
}

// leaseDBAt is withDBAt that keeps the connection open for the calls that
// follow (see dbConnection.with).
func leaseDBAt(options nteedb.Options, use connectionUse, run func(db *nteedb.DB) error) error {
	use.lease = true
	return connectionAt(options.Dir).with(options, use, run)
}

// with runs run on the connection, opening it first when it is closed or its
// options changed. A lease keeps it open until its last session ends or it
// sat idle for idleClose. A connection that broke between calls is reopened
// once and run again: every run in this package only gets, puts and deletes,
// so running it twice is safe.
func (c *dbConnection) with(options nteedb.Options, use connectionUse, run func(db *nteedb.DB) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if use.begin != "" {
		c.active[use.begin] = true
	}
	if c.db != nil && !sameIndexes(c.options.Indexes, options.Indexes) {
		c.closeLocked()
	}
	reused := c.db != nil
	if !reused {
		if err := c.openLocked(options); err != nil {
			c.finishLocked(use, err)
			return err
		}
	}
	err := run(c.db)
	if err != nil && reused && connectionBroken(err) {
		c.closeLocked()
		if openErr := c.openLocked(options); openErr != nil {
			c.finishLocked(use, openErr)
			return openErr
		}
		c.updateStats(func(stats *ConnectionStatus) { stats.Reconnects++ })
		err = run(c.db)
	}
	c.finishLocked(use, err)
	return err
}

// finishLocked settles the connection after a call: per-call connections
// close, and so does a lease whose last session just ended.
func (c *dbConnection) finishLocked(use connectionUse, err error) {
	if use.end != "" {
		delete(c.active, use.end)
	}
	now := time.Now()
	c.lastUsed = now
	if use.lease && c.db != nil {
		c.leased = true
	}
	lastSessionEnded := use.end != "" && len(c.active) == 0
	if c.db != nil && (!c.leased || lastSessionEnded) {
		c.closeLocked()
	}
	if c.db != nil {
		if c.idle == nil {
			c.idle = time.AfterFunc(c.idleClose, c.closeWhenIdle)
		} else {
			c.idle.Reset(c.idleClose)
		}
	}
	c.updateStats(func(stats *ConnectionStatus) {
		stats.Calls++
		stats.LastUsed = now
		stats.Sessions = len(c.active)
		stats.ClosesAt = time.Time{}
		if c.db != nil {
			stats.ClosesAt = now.Add(c.idleClose)
		}
		if err != nil {
			stats.LastError = err.Error()
		}
	})
}

func (c *dbConnection) closeWhenIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return
	}
	if waited := time.Since(c.lastUsed); waited < c.idleClose {
		c.idle.Reset(c.idleClose - waited)
		return
	}
	c.closeLocked()
}

func (c *dbConnection) openLocked(options nteedb.Options) error {
	if err := os.MkdirAll(options.Dir, 0o755); err != nil {
		return err
	}
	var db *nteedb.DB
	var err error
	for attempt := 0; attempt < globalLockRetries; attempt++ {
		db, err = nteedb.Open(options)
		if !errors.Is(err, nteedb.ErrLocked) {
			break
		}
		time.Sleep(globalLockBackoff * time.Duration(attempt+1))
	}
	if err != nil {
		return err
	}
	c.db, c.options = db, options
	now := time.Now()
	c.updateStats(func(stats *ConnectionStatus) {
		stats.Open, stats.OpenedAt = true, now
		stats.Opens++
	})
	return nil
}

// closeLocked closes the connection, which writes ntee-db's hint and
// flushes the log to disk.
func (c *dbConnection) closeLocked() {
	if c.db == nil {
		return
	}
	err := c.db.Close()
	c.db, c.leased = nil, false
	if c.idle != nil {
		c.idle.Stop()
	}
	c.updateStats(func(stats *ConnectionStatus) {
		stats.Open, stats.ClosesAt = false, time.Time{}
		if err != nil {
			stats.LastError = err.Error()
		}
	})
}

func (c *dbConnection) updateStats(update func(stats *ConnectionStatus)) {
	c.statsMu.Lock()
	update(&c.stats)
	c.statsMu.Unlock()
}

func (c *dbConnection) status(name string, mode ConnectionMode) ConnectionStatus {
	c.statsMu.Lock()
	status := c.stats
	c.statsMu.Unlock()
	status.Name, status.Mode = name, mode
	return status
}

func connectionStatusAt(dir, name string, mode ConnectionMode) ConnectionStatus {
	return connectionAt(dir).status(name, mode)
}

// CloseConnections closes every connection still open, so the last writes
// are flushed and the next start reads a fresh hint. Called at quit.
func CloseConnections() {
	connections.Range(func(_, value any) bool {
		connection := value.(*dbConnection)
		connection.mu.Lock()
		connection.closeLocked()
		connection.mu.Unlock()
		return true
	})
}

// connectionBroken tells an error of the connection itself (closed under
// the call, a file gone) from one of the call's own data.
func connectionBroken(err error) bool {
	var pathError *fs.PathError
	return errors.Is(err, nteedb.ErrClosed) || errors.Is(err, os.ErrClosed) || errors.As(err, &pathError)
}

func sameIndexes(left, right []nteedb.IndexDef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Name != right[index].Name || left[index].Kind != right[index].Kind ||
			left[index].MaxPerValue != right[index].MaxPerValue || (left[index].Extract == nil) != (right[index].Extract == nil) {
			return false
		}
	}
	return true
}
