package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/store"
)

// fakeMaintBackend wraps Memory with scriptable maintenance methods.
type fakeMaintBackend struct {
	*store.Memory
	info      store.DBInfo
	compacted int
	relieved  int
	fail      error
}

func (f *fakeMaintBackend) Maintenance() (store.DBInfo, error) { return f.info, f.fail }
func (f *fakeMaintBackend) Compact() error                     { f.compacted++; return f.fail }
func (f *fakeMaintBackend) RelieveBlobs() error                { f.relieved++; return f.fail }

// fakeRegistry records Enable/Disable calls and serves scripted statuses.
type fakeRegistry struct {
	statuses   []lsp.LangStatus
	enabled    []string
	disabled   []string
	failEnable string // language whose Enable fails
}

func (f *fakeRegistry) ClientFor(string) (lsp.Client, bool) { return nil, false }
func (f *fakeRegistry) UnavailableReason(string) string     { return "" }
func (f *fakeRegistry) Statuses() []lsp.LangStatus          { return f.statuses }
func (f *fakeRegistry) Enable(lang string) (bool, string) {
	if lang == f.failEnable {
		return false, lang + "-lsp not found — try: ntee --prepare-lsp"
	}
	f.enabled = append(f.enabled, lang)
	return true, ""
}
func (f *fakeRegistry) Disable(lang string) { f.disabled = append(f.disabled, lang) }
func (f *fakeRegistry) ShutdownAll()        {}

// drain feeds a returned tea.Cmd's message back through Update, like the
// Bubble Tea runtime would.
func drain(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	next, _ := m.Update(cmd())
	return next.(Model)
}

func TestInspectEnterExitRoundTrip(t *testing.T) {
	m, _ := newTestModel(t, nil)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = next.(Model)
	if m.mode != modeInspect || m.inspectPrevMode != modeQuery {
		t.Fatalf("Ctrl+T from query: mode=%v prev=%v", m.mode, m.inspectPrevMode)
	}
	if cmd == nil {
		t.Fatal("entering inspect should fetch stats")
	}
	m = ctrl(m, tea.KeyEsc)
	if m.mode != modeQuery {
		t.Fatalf("Esc should restore query mode, got %v", m.mode)
	}

	// From edit mode, and the editor is left untouched.
	m = m.openFileAt("main.go")
	m = ctrl(m, tea.KeyCtrlT)
	if m.mode != modeInspect || m.inspectPrevMode != modeEdit {
		t.Fatalf("Ctrl+T from edit: mode=%v prev=%v", m.mode, m.inspectPrevMode)
	}
	m = ctrl(m, tea.KeyEsc)
	if m.mode != modeEdit {
		t.Fatalf("Esc should restore edit mode, got %v", m.mode)
	}
}

func TestInspectBlocksGlobalChords(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = ctrl(m, tea.KeyCtrlT)
	m = ctrl(m, tea.KeyCtrlP)
	if m.fuzzyOpen {
		t.Fatal("Ctrl+P must not open the finder in inspection mode")
	}
	m = ctrl(m, tea.KeyCtrlG)
	if m.grepOpen {
		t.Fatal("Ctrl+G must not open grep in inspection mode")
	}
}

func TestInspectMenuSelection(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = ctrl(m, tea.KeyCtrlT)
	if m.inspectMenu != inspectMenuDB {
		t.Fatalf("menu should start at ntee-db, got %d", m.inspectMenu)
	}
	m = ctrl(m, tea.KeyShiftDown)
	if m.inspectMenu != inspectMenuLSP {
		t.Fatalf("Shift+Down should select lsp, got %d", m.inspectMenu)
	}
	m = ctrl(m, tea.KeyShiftDown) // clamped
	if m.inspectMenu != inspectMenuLSP {
		t.Fatalf("selection should clamp at the last item, got %d", m.inspectMenu)
	}
	m = ctrl(m, tea.KeyShiftUp)
	m = ctrl(m, tea.KeyShiftUp) // clamped
	if m.inspectMenu != inspectMenuDB {
		t.Fatalf("selection should clamp at the first item, got %d", m.inspectMenu)
	}
}

func TestInspectUnknownCommand(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = ctrl(m, tea.KeyCtrlT)
	m = runes(m, "bogus x")
	m = ctrl(m, tea.KeyEnter)
	if m.errText != "unknown command: bogus" || m.mode != modeInspect {
		t.Fatalf("errText=%q mode=%v", m.errText, m.mode)
	}
}

func TestInspectDBMemoryFallback(t *testing.T) {
	m, _ := newTestModel(t, nil) // Memory backend
	m = ctrl(m, tea.KeyCtrlT)
	m = drain(t, m, m.fetchDBInfoCmd())
	if !errors.Is(m.inspectInfoErr, store.ErrNoStats) {
		t.Fatalf("inspectInfoErr = %v", m.inspectInfoErr)
	}
	if out := m.View(); !strings.Contains(out, "no statistics") {
		t.Fatal("db pane should show the in-memory fallback")
	}
	m = runes(m, "db compact")
	next, cmd := m.runInspectCommand("db compact")
	m = next.(Model)
	if cmd != nil || !strings.Contains(m.errText, "in-memory store") {
		t.Fatalf("compact on memory store: cmd=%v errText=%q", cmd, m.errText)
	}
}

func TestInspectDBCompactFlow(t *testing.T) {
	m, _ := newTestModel(t, nil)
	fake := &fakeMaintBackend{Memory: store.NewMemory(), info: store.DBInfo{Records: 7, MainBytes: 100, LiveBytes: 60}}
	m.db = fake
	m = ctrl(m, tea.KeyCtrlT)
	m = drain(t, m, m.fetchDBInfoCmd())
	if m.inspectInfo.Records != 7 || m.inspectLoading {
		t.Fatalf("stats not stored: %+v loading=%v", m.inspectInfo, m.inspectLoading)
	}
	if out := m.View(); !strings.Contains(out, "records      7") {
		t.Fatal("db pane should render the record count in the aligned column")
	}

	m = runes(m, "db compact")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.inspectBusy != "compact" || m.notice != "db compact started" {
		t.Fatalf("busy=%q notice=%q", m.inspectBusy, m.notice)
	}

	// Duplicate run blocked while busy.
	next2, cmd2 := m.runInspectCommand("db relieve")
	if m2 := next2.(Model); cmd2 != nil || !strings.Contains(m2.errText, "already running") {
		t.Fatalf("duplicate maintenance not blocked: %q", m2.errText)
	}

	m = drain(t, m, cmd) // run the compact, feed the msg back
	if fake.compacted != 1 {
		t.Fatalf("compacted %d times", fake.compacted)
	}
	if m.inspectBusy != "" || m.notice != "db compact done" || !m.inspectLoading {
		t.Fatalf("after compact: busy=%q notice=%q loading=%v", m.inspectBusy, m.notice, m.inspectLoading)
	}
}

func TestInspectDBCompactError(t *testing.T) {
	m, _ := newTestModel(t, nil)
	fake := &fakeMaintBackend{Memory: store.NewMemory(), fail: errors.New("disk full")}
	m.db = fake
	m.mode = modeInspect
	next, cmd := m.runInspectCommand("db compact")
	m = drain(t, next.(Model), cmd)
	if !strings.Contains(m.errText, "disk full") || m.inspectBusy != "" {
		t.Fatalf("errText=%q busy=%q", m.errText, m.inspectBusy)
	}
}

func TestInspectLSPEnablePersistsAndStarts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m, _ := newTestModel(t, nil)
	reg := &fakeRegistry{}
	m.lsp = reg
	m = ctrl(m, tea.KeyCtrlT)

	m = runes(m, "lsp enable typescript")
	m = ctrl(m, tea.KeyEnter)
	if !strings.Contains(m.notice, "lsp enabled: typescript") {
		t.Fatalf("notice = %q errText = %q", m.notice, m.errText)
	}
	if len(reg.enabled) != 1 || reg.enabled[0] != "typescript" {
		t.Fatalf("Enable calls = %v", reg.enabled)
	}
	if m.inspectMenu != inspectMenuLSP {
		t.Fatal("enable should switch to the lsp pane")
	}

	path, err := config.ConfigPath()
	must(t, err)
	data, err := os.ReadFile(path)
	must(t, err)
	var file config.Config
	must(t, yaml.Unmarshal(data, &file))
	lc := file.Languages["typescript"]
	if lc.Enabled == nil || !*lc.Enabled {
		t.Fatalf("config not persisted: %+v", lc)
	}
}

func TestInspectLSPDisableAll(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m, _ := newTestModel(t, nil)
	reg := &fakeRegistry{}
	m.lsp = reg
	m.mode = modeInspect

	next, _ := m.runInspectCommand("lsp disable all")
	m = next.(Model)
	if !strings.Contains(m.notice, "lsp disabled:") {
		t.Fatalf("notice = %q errText = %q", m.notice, m.errText)
	}
	want := m.knownLanguages()
	if len(reg.disabled) != len(want) {
		t.Fatalf("Disable calls = %v, want all of %v", reg.disabled, want)
	}

	path, err := config.ConfigPath()
	must(t, err)
	data, err := os.ReadFile(path)
	must(t, err)
	var file config.Config
	must(t, yaml.Unmarshal(data, &file))
	for _, lang := range want {
		lc := file.Languages[lang]
		if lc.Enabled == nil || *lc.Enabled {
			t.Fatalf("%s not persisted as disabled: %+v", lang, lc)
		}
	}
	if !file.LSP.Enabled {
		t.Fatal("disable all must not flip the global lsp.enabled flag")
	}
}

func TestInspectLSPEnableAllSetsGlobalFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m, _ := newTestModel(t, nil)
	m.lsp = &fakeRegistry{}
	m.mode = modeInspect

	next, _ := m.runInspectCommand("lsp enable all")
	m = next.(Model)
	if m.errText != "" {
		t.Fatalf("errText = %q", m.errText)
	}
	path, err := config.ConfigPath()
	must(t, err)
	data, err := os.ReadFile(path)
	must(t, err)
	var file config.Config
	must(t, yaml.Unmarshal(data, &file))
	if !file.LSP.Enabled {
		t.Fatal("enable all should set global lsp.enabled true")
	}
}

func TestInspectLSPEnableFailureStays(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m, _ := newTestModel(t, nil)
	m.lsp = &fakeRegistry{failEnable: "go"}
	m.mode = modeInspect

	next, _ := m.runInspectCommand("lsp enable go")
	m = next.(Model)
	if !strings.Contains(m.errText, "not found") || m.mode != modeInspect {
		t.Fatalf("errText=%q mode=%v", m.errText, m.mode)
	}
}

func TestInspectLSPUnknownLanguage(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.mode = modeInspect
	next, _ := m.runInspectCommand("lsp enable cobol")
	m = next.(Model)
	if !strings.Contains(m.errText, "unknown language: cobol") || !strings.Contains(m.errText, "go") {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestInspectLSPPaneRendersStatuses(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.lsp = &fakeRegistry{statuses: []lsp.LangStatus{
		{Lang: "go", State: lsp.LangRunning},
		{Lang: "ruby", State: lsp.LangStopped},
		{Lang: "typescript", State: lsp.LangDisabled, Reason: "disabled in config"},
	}}
	m.mode = modeInspect
	m.inspectMenu = inspectMenuLSP
	out := m.View()
	for _, want := range []string{"go", "running", "ruby", "stopped", "typescript", "disabled in config", "@inspection >"} {
		if !strings.Contains(out, want) {
			t.Fatalf("View missing %q", want)
		}
	}
	// Names are padded to one column, so even the longest gets a gap before
	// its state and all states start at the same offset.
	for _, want := range []string{"go          running", "ruby        stopped", "typescript  disabled"} {
		if !strings.Contains(out, want) {
			t.Fatalf("View missing aligned row %q", want)
		}
	}
}

func TestInspectConfigBackupWritten(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	// Seed an existing config so the write produces a .bak.
	dir := filepath.Join(xdg, "ntee-editor")
	must(t, os.MkdirAll(dir, 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: 1\n"), 0o644))

	m, _ := newTestModel(t, nil)
	m.lsp = &fakeRegistry{}
	m.mode = modeInspect
	next, _ := m.runInspectCommand("lsp disable go")
	if m = next.(Model); m.errText != "" {
		t.Fatalf("errText = %q", m.errText)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml.bak")); err != nil {
		t.Fatalf("backup not written: %v", err)
	}
}
