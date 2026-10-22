package app

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/filetree"
)

// WithOnlyFile scopes the editor to a single file (`ntee <file>`): the
// sidebar tree, the search index behind Ctrl+P / Ctrl+G / Ctrl+U and the query
// popup all hold just rel, so the folder's other files stay out of sight. The
// index is set here, so the folder is never walked and no nested-repo scan
// runs. Apply it before the program starts (Init reads it).
func (m Model) WithOnlyFile(rel string) Model {
	m.onlyFile = rel
	m.corpus, m.dirCorpus = []string{rel}, nil
	m.queryPrepared = filetree.PrepareCorpus(m.corpus, nil)
	m.corpusBuiltAt = time.Now()
	m.corpusRebuilding, m.corpusTruncated = false, false
	m.pendingValidate = nil
	m.splash = false // nothing to index
	m.workspaceRepos, m.workspaceReposScanned = nil, true
	return m
}

// onlyFileCorpusCmd is rebuildCorpusCmd in single-file mode: the index is the
// one file, with no walk and no repo scan.
func (m Model) onlyFileCorpusCmd() tea.Cmd {
	files, rootGen := []string{m.onlyFile}, m.rootGen
	return func() tea.Msg {
		return corpusMsg{
			files:    files,
			prepared: filetree.PrepareCorpus(files, nil),
			builtAt:  time.Now(),
			rootGen:  rootGen,
		}
	}
}
