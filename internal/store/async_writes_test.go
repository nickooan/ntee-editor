package store

import (
	"sync"
	"testing"
)

// gatedMemory is a goroutine-safe Memory whose writes wait for gate, so a
// test can look at the writes still queued.
type gatedMemory struct {
	*Memory
	mu   sync.Mutex
	gate chan struct{}
}

func (g *gatedMemory) SnapshotPut(path string, seq int64, kind, content string) error {
	<-g.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Memory.SnapshotPut(path, seq, kind, content)
}

func (g *gatedMemory) SnapshotGet(seq int64) (Snapshot, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Memory.SnapshotGet(seq)
}

func (g *gatedMemory) SnapshotDelete(seqs []int64) {
	<-g.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Memory.SnapshotDelete(seqs)
}

func (g *gatedMemory) LastSave(path string) (Snapshot, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Memory.LastSave(path)
}

func (g *gatedMemory) SaveCorpus(c CorpusIndex) error {
	<-g.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Memory.SaveCorpus(c)
}

func (g *gatedMemory) LoadCorpus() (CorpusIndex, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.Memory.LoadCorpus()
}

func newGatedWrites() (*AsyncWrites, *gatedMemory) {
	inner := &gatedMemory{Memory: NewMemory(), gate: make(chan struct{})}
	return NewAsyncWrites(inner), inner
}

func TestAsyncWritesReadTheirOwnQueuedSnapshots(t *testing.T) {
	writes, inner := newGatedWrites()
	writes.SnapshotPut("a.go", 1, "edit", "one")
	writes.SnapshotPut("a.go", 2, "edit", "two")
	writes.SnapshotDelete([]int64{1})
	if snapshot, ok := writes.SnapshotGet(2); !ok || snapshot.Content != "two" {
		t.Fatalf("a queued put reads back at once: %+v %v", snapshot, ok)
	}
	if _, ok := writes.SnapshotGet(1); ok {
		t.Fatal("a queued delete hides the snapshot at once")
	}
	if _, ok := inner.SnapshotGet(2); ok {
		t.Fatal("nothing is written while the writer waits")
	}
	close(inner.gate)
	if snapshot, ok := writes.LastSave("a.go"); ok {
		t.Fatalf("no save yet: %+v", snapshot)
	}
	if snapshot, ok := inner.SnapshotGet(2); !ok || snapshot.Content != "two" {
		t.Fatal("a scanning read drains the queue first")
	}
	if _, ok := inner.SnapshotGet(1); ok {
		t.Fatal("writes land in order: the delete after its put")
	}
	if _, ok := writes.SnapshotGet(2); !ok {
		t.Fatal("a written snapshot still reads")
	}
	must(t, writes.Close())
}

func TestAsyncWritesKeepTheLatestPutOfASeq(t *testing.T) {
	writes, inner := newGatedWrites()
	writes.SnapshotPut("a.go", 1, "edit", "one")
	writes.SnapshotPut("a.go", 1, "save", "one")
	if snapshot, _ := writes.SnapshotGet(1); snapshot.Kind != "save" {
		t.Fatalf("the upgrade reads back: %+v", snapshot)
	}
	close(inner.gate)
	if snapshot, ok := writes.LastSave("a.go"); !ok || snapshot.Seq != 1 {
		t.Fatalf("the save lands: %+v %v", snapshot, ok)
	}
	must(t, writes.Close())
}

func TestAsyncWritesSaveTheCorpusOffTheCaller(t *testing.T) {
	writes, inner := newGatedWrites()
	must(t, writes.SaveCorpus(CorpusIndex{Version: CorpusVersion, Files: []string{"a.go"}}))
	close(inner.gate)
	if corpus, ok := writes.LoadCorpus(); !ok || len(corpus.Files) != 1 {
		t.Fatalf("a load sees the queued save: %+v %v", corpus, ok)
	}
	must(t, writes.Close())
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
