package store

import "sync"

// asyncWriteQueueSize bounds the writes waiting for the writer goroutine;
// past it a write waits for room, as it would have waited for the db.
const asyncWriteQueueSize = 64

// AsyncWrites is a Backend whose heavy, frequent writes — undo snapshots
// (the whole buffer, every typing burst) and the search corpus (up to 50k
// paths) — are made by one writer goroutine, in the order they were asked
// for, so the UI goroutine never marshals or writes them. Reads see every
// write still queued: SnapshotGet answers from the queued snapshots, and the
// reads that scan the db (LastSave, LoadCorpus, maintenance) wait for the
// queue to drain first. Every other call passes straight through.
type AsyncWrites struct {
	Backend
	queue chan func()
	done  chan struct{}

	mu             sync.Mutex
	pendingPuts    map[int64]pendingSnapshot
	pendingDeletes map[int64]int // seq → deletes queued for it
	putVersion     int64
}

// pendingSnapshot is a queued SnapshotPut; version tells a re-put of the
// same seq (a "save" upgrade) from the one being written.
type pendingSnapshot struct {
	snapshot Snapshot
	version  int64
}

// NewAsyncWrites wraps inner and starts its writer goroutine; Close drains
// the queue, then closes inner.
func NewAsyncWrites(inner Backend) *AsyncWrites {
	writes := &AsyncWrites{
		Backend:        inner,
		queue:          make(chan func(), asyncWriteQueueSize),
		done:           make(chan struct{}),
		pendingPuts:    map[int64]pendingSnapshot{},
		pendingDeletes: map[int64]int{},
	}
	go writes.write()
	return writes
}

func (w *AsyncWrites) write() {
	defer close(w.done)
	for run := range w.queue {
		run()
	}
}

func (w *AsyncWrites) enqueue(run func()) { w.queue <- run }

// Connections reports the wrapped store's connections.
func (w *AsyncWrites) Connections() []ConnectionStatus {
	if reporter, ok := w.Backend.(ConnectionReporter); ok {
		return reporter.Connections()
	}
	return nil
}

// flush waits until every write queued before it is in the db: the writer
// reaches its marker only after them. Safe from any goroutine.
func (w *AsyncWrites) flush() {
	reached := make(chan struct{})
	w.queue <- func() { close(reached) }
	<-reached
}

func (w *AsyncWrites) SnapshotPut(path string, seq int64, kind, content string) error {
	w.mu.Lock()
	w.putVersion++
	version := w.putVersion
	w.pendingPuts[seq] = pendingSnapshot{snapshot: Snapshot{Path: path, Seq: seq, Kind: kind, Content: content, At: seq}, version: version}
	w.mu.Unlock()
	w.enqueue(func() {
		_ = w.Backend.SnapshotPut(path, seq, kind, content)
		w.mu.Lock()
		if pending, ok := w.pendingPuts[seq]; ok && pending.version == version {
			delete(w.pendingPuts, seq)
		}
		w.mu.Unlock()
	})
	return nil
}

func (w *AsyncWrites) SnapshotDelete(seqs []int64) {
	if len(seqs) == 0 {
		return
	}
	seqs = append([]int64(nil), seqs...)
	w.mu.Lock()
	for _, seq := range seqs {
		delete(w.pendingPuts, seq)
		w.pendingDeletes[seq]++
	}
	w.mu.Unlock()
	w.enqueue(func() {
		w.Backend.SnapshotDelete(seqs)
		w.mu.Lock()
		for _, seq := range seqs {
			if w.pendingDeletes[seq]--; w.pendingDeletes[seq] <= 0 {
				delete(w.pendingDeletes, seq)
			}
		}
		w.mu.Unlock()
	})
}

func (w *AsyncWrites) SnapshotGet(seq int64) (Snapshot, bool) {
	w.mu.Lock()
	pending, queued := w.pendingPuts[seq]
	deleted := w.pendingDeletes[seq] > 0
	w.mu.Unlock()
	switch {
	case queued:
		return pending.snapshot, true
	case deleted:
		return Snapshot{}, false
	}
	return w.Backend.SnapshotGet(seq)
}

func (w *AsyncWrites) LastSave(path string) (Snapshot, bool) {
	w.flush()
	return w.Backend.LastSave(path)
}

func (w *AsyncWrites) SaveCorpus(c CorpusIndex) error {
	w.enqueue(func() { _ = w.Backend.SaveCorpus(c) })
	return nil
}

func (w *AsyncWrites) SaveCorpusFor(scope string, c CorpusIndex) error {
	w.enqueue(func() { _ = w.Backend.SaveCorpusFor(scope, c) })
	return nil
}

func (w *AsyncWrites) LoadCorpus() (CorpusIndex, bool) {
	w.flush()
	return w.Backend.LoadCorpus()
}

func (w *AsyncWrites) LoadCorpusFor(scope string) (CorpusIndex, bool) {
	w.flush()
	return w.Backend.LoadCorpusFor(scope)
}

func (w *AsyncWrites) Maintenance() (DBInfo, error) {
	return w.Backend.Maintenance() // stats only: a queued write changes nothing it relies on
}

func (w *AsyncWrites) Compact() error {
	w.flush()
	return w.Backend.Compact()
}

func (w *AsyncWrites) RelieveBlobs() error {
	w.flush()
	return w.Backend.RelieveBlobs()
}

// Close writes what is queued, stops the writer, and closes the inner
// backend. Nothing may be called after it.
func (w *AsyncWrites) Close() error {
	close(w.queue)
	<-w.done
	return w.Backend.Close()
}
