package store

import (
	"errors"
	"testing"
	"time"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

// testConnectionDir is a fresh directory whose connection closes after idle
// and is closed when the test ends.
func testConnectionDir(t *testing.T, idle time.Duration) string {
	t.Helper()
	dir := t.TempDir()
	connection := connectionAt(dir)
	connection.mu.Lock()
	connection.idleClose = idle
	connection.mu.Unlock()
	t.Cleanup(func() {
		connection.mu.Lock()
		connection.closeLocked()
		connection.mu.Unlock()
		connections.Delete(dir)
	})
	return dir
}

// lockedOut reports whether another opener is locked out of dir, the
// way another ntee window would be.
func lockedOut(t *testing.T, dir string) bool {
	t.Helper()
	db, err := nteedb.Open(nteedb.Options{Dir: dir})
	if errors.Is(err, nteedb.ErrLocked) {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	return false
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func editLogStatus(log *AgentEditLog) ConnectionStatus { return log.Connections()[0] }

func TestAnEditSessionKeepsOneConnectionUntilItFinishes(t *testing.T) {
	dir := testConnectionDir(t, time.Hour)
	log := &AgentEditLog{dir: dir}
	session := agentEditSession("s1", time.Unix(1_800_000_000, 0))
	for range 3 {
		if err := log.SavePendingAgentEditFiles(session, []string{"a.go"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := log.LoadPendingAgentEdits(); err != nil {
		t.Fatal(err)
	}
	status := editLogStatus(log)
	if !status.Open || status.Opens != 1 || status.Calls != 4 || status.Sessions != 1 || status.Mode != ConnectionSessionLease || status.ClosesAt.IsZero() {
		t.Fatalf("the session's edits share one open connection: %+v", status)
	}
	if !lockedOut(t, dir) {
		t.Fatal("the open connection holds the db")
	}
	session.Status, session.EndedAt = AgentEditDiscarded, session.StartedAt.Add(time.Minute)
	if err := log.FinishAgentEdit(session, 5); err != nil {
		t.Fatal(err)
	}
	if status = editLogStatus(log); status.Open || status.Sessions != 0 || !status.ClosesAt.IsZero() {
		t.Fatalf("finishing the last session closes the connection: %+v", status)
	}
	if lockedOut(t, dir) {
		t.Fatal("a closed connection leaves the db to others")
	}
	if finished, err := (&AgentEditLog{dir: dir}).LoadFinishedAgentEdits(); err != nil || len(finished) != 1 {
		t.Fatalf("what the lease wrote is on disk: %+v %v", finished, err)
	}
}

func TestSessionsOfSeveralAgentsShareTheConnection(t *testing.T) {
	dir := testConnectionDir(t, time.Hour)
	first, second := &AgentEditLog{dir: dir}, &AgentEditLog{dir: dir}
	base := time.Unix(1_800_000_000, 0)
	one, two := agentEditSession("one", base), agentEditSession("two", base.Add(time.Second))
	if err := first.SavePendingAgentEdit(one); err != nil {
		t.Fatal(err)
	}
	if err := second.SavePendingAgentEdit(two); err != nil {
		t.Fatal(err)
	}
	if status := editLogStatus(first); status.Opens != 1 || status.Sessions != 2 {
		t.Fatalf("two sessions, one connection: %+v", status)
	}
	one.Status, one.EndedAt = AgentEditApplied, base.Add(time.Minute)
	if err := first.FinishAgentEdit(one, 5); err != nil {
		t.Fatal(err)
	}
	if status := editLogStatus(first); !status.Open || status.Sessions != 1 {
		t.Fatalf("the other session keeps it open: %+v", status)
	}
	two.Status, two.EndedAt = AgentEditApplied, base.Add(2*time.Minute)
	if err := second.FinishAgentEdit(two, 5); err != nil {
		t.Fatal(err)
	}
	if status := editLogStatus(first); status.Open {
		t.Fatalf("the last session to finish closes it: %+v", status)
	}
}

func TestAnIdleLeaseCloses(t *testing.T) {
	dir := testConnectionDir(t, 30*time.Millisecond)
	history := &ExternalHistory{dir: dir}
	appendRecords(t, history, 2, 10)
	if status := history.Connections()[0]; !status.Open || status.Opens != 1 {
		t.Fatalf("a burst of requests shares one open: %+v", status)
	}
	waitUntil(t, "an idle lease closes", func() bool { return !history.Connections()[0].Open })
	if lockedOut(t, dir) {
		t.Fatal("a lease closed when idle leaves the db to others")
	}
	if records, err := history.LoadExternal(); err != nil || len(records) != 2 {
		t.Fatalf("the next call reopens it: %d %v", len(records), err)
	}
}

func TestAHistoryLimitChangeReopensOnlyOnTheWriteThatBringsIt(t *testing.T) {
	dir := testConnectionDir(t, time.Hour)
	history := &ExternalHistory{dir: dir}
	keys := appendRecords(t, history, 5, 10)
	if _, err := history.LoadExternal(); err != nil {
		t.Fatal(err)
	}
	if err := history.UpdateExternalStatus(keys[0], ExternalOpened, ""); err != nil {
		t.Fatal(err)
	}
	if status := history.Connections()[0]; status.Opens != 1 {
		t.Fatalf("reads and status updates use the limit last given: %+v", status)
	}
	appendRecords(t, history, 1, 3)
	records, _ := history.LoadExternal()
	if status := history.Connections()[0]; status.Opens != 2 || len(records) != 3 {
		t.Fatalf("a lowered limit reopens and evicts on that write: %+v, %d records", status, len(records))
	}
}

func TestABrokenConnectionReopensOnce(t *testing.T) {
	dir := testConnectionDir(t, time.Hour)
	history := &ExternalHistory{dir: dir}
	appendRecords(t, history, 1, 10)
	connection := connectionAt(dir)
	connection.mu.Lock()
	connection.db.Close() // broken under the lease
	connection.mu.Unlock()
	records, err := history.LoadExternal()
	if status := history.Connections()[0]; err != nil || len(records) != 1 || status.Reconnects != 1 || !status.Open {
		t.Fatalf("the call reopens and runs again: %d %v %+v", len(records), err, status)
	}
}

func TestAReopenThatStaysLockedReturnsTheError(t *testing.T) {
	dir := testConnectionDir(t, time.Hour)
	history := &ExternalHistory{dir: dir}
	appendRecords(t, history, 1, 10)
	connection := connectionAt(dir)
	connection.mu.Lock()
	connection.db.Close()
	connection.mu.Unlock()
	holder, err := nteedb.Open(nteedb.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	_, err = history.LoadExternal()
	if status := history.Connections()[0]; !errors.Is(err, nteedb.ErrLocked) || status.Open || status.LastError == "" {
		t.Fatalf("a reopen another process blocks fails the call: %v %+v", err, status)
	}
}

func TestAPerCallConnectionClosesAfterEachCall(t *testing.T) {
	dir := testConnectionDir(t, time.Hour)
	settings := NewGlobalExternalSettings(dir)
	for range 2 {
		if _, err := settings.LoadExternalSettings(); err != nil {
			t.Fatal(err)
		}
	}
	if status := settings.Connections()[0]; status.Open || status.Opens != 2 || status.Calls != 2 || status.Mode != ConnectionPerCall {
		t.Fatalf("the global db opens per call: %+v", status)
	}
	if lockedOut(t, dir) {
		t.Fatal("a per-call connection never stays open")
	}
}

func TestAStatusNeverWaitsForACallInFlight(t *testing.T) {
	dir := testConnectionDir(t, time.Hour)
	log := &AgentEditLog{dir: dir}
	inside, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = log.withDB(func(*nteedb.DB) error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside
	defer close(release)
	reported := make(chan ConnectionStatus, 1)
	go func() { reported <- editLogStatus(log) }()
	select {
	case status := <-reported:
		if !status.Open {
			t.Fatalf("the call in flight has it open: %+v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("a status snapshot waited for the db call")
	}
}

// countingGlobalSettings counts the loads that reach the db.
type countingGlobalSettings struct {
	*GlobalExternalSettings
	loads int
}

func (c *countingGlobalSettings) LoadExternalSettings() (ExternalSettings, error) {
	c.loads++
	return c.GlobalExternalSettings.LoadExternalSettings()
}

func TestTheSettingsCacheReloadsOnlyWhenTheDBChanged(t *testing.T) {
	dir := testConnectionDir(t, time.Hour)
	// A brand-new db's log appears on its first open, which reads as a
	// change once; the global db the editor uses exists long before.
	otherWindow := NewGlobalExternalSettings(dir)
	if err := otherWindow.SaveExternalSettings(DefaultExternalSettings); err != nil {
		t.Fatal(err)
	}
	inner := &countingGlobalSettings{GlobalExternalSettings: NewGlobalExternalSettings(dir)}
	cached := NewCachedExternalSettings(inner)
	cached.maxAge = 0 // only the stamp keeps it
	for range 3 {
		if _, err := cached.LoadExternalSettings(); err != nil {
			t.Fatal(err)
		}
	}
	if inner.loads != 1 {
		t.Fatalf("an unchanged db is read once: %d", inner.loads)
	}
	if statuses := cached.Connections(); len(statuses) != 1 || statuses[0].Note != "settings cache fresh" {
		t.Fatalf("the global row says the cache is fresh: %+v", statuses)
	}
	if err := otherWindow.SaveExternalSettings(ExternalSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if settings, err := cached.LoadExternalSettings(); err != nil || !settings.Enabled || inner.loads != 2 {
		t.Fatalf("another window's save shows on the next load: %+v %v (%d loads)", settings, err, inner.loads)
	}
	cached.stampedAge = 0
	if _, err := cached.LoadExternalSettings(); err != nil || inner.loads != 3 {
		t.Fatalf("the stamp vouches for a bounded while only: %d", inner.loads)
	}
}
