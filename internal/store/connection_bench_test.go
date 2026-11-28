package store

import (
	"fmt"
	"os"
	"testing"
	"time"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

// The connection cost runs as a gated test: NTEE_STORE_BENCH=1 go test
// ./internal/store -run TestConnectionCost -v
func TestConnectionCost(t *testing.T) {
	if os.Getenv("NTEE_STORE_BENCH") == "" {
		t.Skip("set NTEE_STORE_BENCH=1 to measure connection cost")
	}
	const calls = 100
	measure := func(name string, call func(dir string, index int) error) {
		dir := testConnectionDir(t, time.Hour)
		started := time.Now()
		for index := range calls {
			if err := call(dir, index); err != nil {
				t.Fatal(err)
			}
		}
		connection := connectionAt(dir)
		connection.mu.Lock()
		connection.closeLocked()
		connection.mu.Unlock()
		elapsed := time.Since(started)
		t.Logf("%-28s %d calls in %v (%v per call)", name, calls, elapsed.Round(time.Millisecond), (elapsed / calls).Round(time.Microsecond))
	}
	put := func(db *nteedb.DB, index int) error {
		return db.Put(fmt.Sprintf("record:%04d", index), []byte(`{"command":"read_file","paths":["main.go"]}`))
	}
	measure("per call (before)", func(dir string, index int) error {
		return withDBAt(nteedb.Options{Dir: dir}, func(db *nteedb.DB) error { return put(db, index) })
	})
	measure("session lease (now)", func(dir string, index int) error {
		return leaseDBAt(nteedb.Options{Dir: dir}, connectionUse{}, func(db *nteedb.DB) error { return put(db, index) })
	})
	session := agentEditSession("bench", time.Unix(1_800_000_000, 0))
	measure("staged edits, lease (now)", func(dir string, index int) error {
		session.Files[0].Content = fmt.Sprintf("new %d\n", index)
		return (&AgentEditLog{dir: dir, apart: map[string]bool{}}).SavePendingAgentEditFiles(session, []string{"a.go"})
	})
}
