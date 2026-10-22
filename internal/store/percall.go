package store

import (
	"errors"
	"os"
	"sync"
	"time"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

const (
	globalLockRetries = 5
	globalLockBackoff = 25 * time.Millisecond
)

// directoryLocks holds one mutex per store directory opened per call, so two
// stores over the same directory in this process (op-commands and external
// settings share the global db) never contend for its flock.
var directoryLocks sync.Map

// withDBAt opens the ntee-db at options.Dir for one call and closes it right
// after, retrying briefly while another editor instance holds the lock for
// its own short read or write. Stores shared across instances use it instead
// of holding the single-writer flock for a whole session. options.Indexes
// must be the same on every open of a directory — an index left out is
// soft-dropped.
func withDBAt(options nteedb.Options, run func(db *nteedb.DB) error) error {
	lock, _ := directoryLocks.LoadOrStore(options.Dir, &sync.Mutex{})
	mutex := lock.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()
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
	runErr := run(db)
	if closeErr := db.Close(); runErr == nil {
		runErr = closeErr
	}
	return runErr
}
