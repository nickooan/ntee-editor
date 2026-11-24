package app

import (
	"strings"
	"testing"

	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/store"
)

func TestGotoCandidatesPutRecentsFirstAndDirectoriesLast(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.corpus = []string{"a.go", "b.go", "c.go"}
	m.dirCorpus = []string{"lib/"}
	m.queryPrepared = filetree.PrepareCorpus(m.corpus, m.dirCorpus)
	must(t, m.db.TouchOpened(store.OpenedFile{Path: "b.go", LastOpenedAt: 1}))
	must(t, m.db.TouchOpened(store.OpenedFile{Path: "c.go", LastOpenedAt: 2}))
	must(t, m.db.TouchOpened(store.OpenedFile{Path: "gone.go", LastOpenedAt: 3}))
	var order []string
	for _, candidate := range m.fuzzyGotoCandidates() {
		order = append(order, candidate.Text)
	}
	if got := strings.Join(order, ","); got != "c.go,b.go,a.go,lib/" {
		t.Fatalf("newest recent first, then the corpus, directories last: %s", got)
	}
	m.queryPrepared = nil // not prepared yet: prepared on the spot
	if got := m.fuzzyGotoCandidates(); len(got) != 4 || got[0].Text != "c.go" {
		t.Fatalf("an unprepared corpus still lists: %+v", got)
	}
}
