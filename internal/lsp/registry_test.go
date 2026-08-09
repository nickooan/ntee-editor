package lsp

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickooan/ntee-editor/internal/config"
)

func boolPtr(b bool) *bool { return &b }

func testManager(t *testing.T) *Manager {
	t.Helper()
	cfg := config.Config{Languages: map[string]config.LanguageConfig{
		"go": {
			Extensions: []string{".go"},
			LSP:        &config.LSPServerConfig{Command: "definitely-not-a-real-lsp-binary"},
		},
		"ruby": {
			Enabled:    boolPtr(false),
			Extensions: []string{".rb"},
			LSP:        &config.LSPServerConfig{Command: "definitely-not-a-real-ruby-lsp"},
		},
	}}
	return NewManager(cfg, t.TempDir())
}

func statusFor(t *testing.T, sts []LangStatus, lang string) LangStatus {
	t.Helper()
	for _, st := range sts {
		if st.Lang == lang {
			return st
		}
	}
	t.Fatalf("no status for %q in %+v", lang, sts)
	return LangStatus{}
}

func TestStatusesInitial(t *testing.T) {
	m := testManager(t)
	sts := m.Statuses()
	if len(sts) != 2 {
		t.Fatalf("want 2 statuses, got %+v", sts)
	}
	if st := statusFor(t, sts, "go"); st.State != LangStopped {
		t.Fatalf("enabled-but-unstarted go should be stopped, got %+v", st)
	}
	if st := statusFor(t, sts, "ruby"); st.State != LangDisabled || st.Reason != "disabled in config" {
		t.Fatalf("config-disabled ruby should be disabled with reason, got %+v", st)
	}
}

func TestDisabledLanguageKeepsExtensionMapping(t *testing.T) {
	m := testManager(t)
	// The extension still maps (extLang is built over all languages), so the
	// user sees the real disable reason instead of the generic install hint.
	if reason := m.UnavailableReason("/x/a.rb"); reason != "disabled in config" {
		t.Fatalf("UnavailableReason = %q", reason)
	}
	if _, ok := m.ClientFor("/x/a.rb"); ok {
		t.Fatal("disabled language must not resolve a client")
	}
}

func TestEnableMissingBinary(t *testing.T) {
	m := testManager(t)
	started, reason := m.Enable("go")
	if started {
		t.Fatal("enable with a missing binary must not report started")
	}
	if !strings.Contains(reason, "not found") {
		t.Fatalf("reason = %q, want binary-not-found", reason)
	}
	if st := statusFor(t, m.Statuses(), "go"); st.State != LangDisabled {
		t.Fatalf("failed enable should leave go disabled, got %+v", st)
	}
}

func TestEnableOverridesConfigDisable(t *testing.T) {
	m := testManager(t)
	// ruby is config-disabled; Enable clears the reason and the override lets
	// getOrStartLocked pass the config gate — it then fails on the missing
	// binary, proving the gate was passed (not the "no server configured" path).
	_, reason := m.Enable("ruby")
	if !strings.Contains(reason, "not found") {
		t.Fatalf("reason = %q, want binary-not-found (config gate must be overridden)", reason)
	}
}

func TestDisableMarksLanguage(t *testing.T) {
	m := testManager(t)
	m.Disable("go")
	if st := statusFor(t, m.Statuses(), "go"); st.State != LangDisabled || st.Reason != "disabled in config" {
		t.Fatalf("disable should mark go disabled, got %+v", st)
	}
	if _, ok := m.ClientFor("/x/a.go"); ok {
		t.Fatal("disabled language must not resolve a client")
	}
}

func TestNoopRegistryStubs(t *testing.T) {
	r := NewNoopRegistry()
	if sts := r.Statuses(); sts != nil {
		t.Fatalf("noop Statuses = %+v, want nil", sts)
	}
	started, reason := r.Enable("go")
	if started || !strings.Contains(reason, "restart") {
		t.Fatalf("noop Enable = (%v, %q)", started, reason)
	}
	r.Disable("go") // must not panic
}

// The sink is program.Send — an unbuffered handoff to the UI goroutine, which
// may itself be calling into the Manager (View → Statuses, Update → ClientFor).
// A notice emitted while m.mu is held therefore deadlocks. This pins the
// collect-then-emit fix with the worst case: a sink that re-enters the Manager.
func TestEmitNeverRunsUnderManagerLock(t *testing.T) {
	m := testManager(t)
	m.SetSink(func(any) {
		// Re-entrant call: deadlocks if the notice was emitted under m.mu.
		m.ClientFor(filepath.Join(m.root, "other.go"))
		m.Statuses()
	})

	done := make(chan struct{})
	go func() {
		// Missing binary → getOrStartLocked produces a "not found" notice.
		m.ClientFor(filepath.Join(m.root, "a.go"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notice emitted while holding m.mu (deadlock)")
	}
}

// projectRootFor memoizes per directory — the second resolve for a sibling
// file must come from the cache (indirectly asserted: cache is populated).
func TestProjectRootForMemoizes(t *testing.T) {
	m := testManager(t)
	a := m.projectRootFor(filepath.Join(m.root, "sub", "a.go"))
	if _, ok := m.rootCache.Load(filepath.Join(m.root, "sub")); !ok {
		t.Fatal("rootCache not populated")
	}
	b := m.projectRootFor(filepath.Join(m.root, "sub", "b.go"))
	if a != b {
		t.Fatalf("sibling files resolved different roots: %q vs %q", a, b)
	}
}

func TestBridgeCycleDisablesLanguage(t *testing.T) {
	mk := func(to string) *config.LSPServerConfig {
		return &config.LSPServerConfig{Command: "x", Bridge: &config.BridgeConfig{To: to, Command: "relay"}}
	}
	// Self-bridge.
	m := NewManager(config.Config{Languages: map[string]config.LanguageConfig{
		"vue": {Extensions: []string{".vue"}, LSP: mk("vue")},
	}}, t.TempDir())
	if !strings.Contains(m.UnavailableReason("/p/a.vue"), "bridge cycle") {
		t.Fatalf("self-bridge not disabled: %q", m.UnavailableReason("/p/a.vue"))
	}

	// Two-cycle: both ends disabled.
	m = NewManager(config.Config{Languages: map[string]config.LanguageConfig{
		"a": {Extensions: []string{".aa"}, LSP: mk("b")},
		"b": {Extensions: []string{".bb"}, LSP: mk("a")},
	}}, t.TempDir())
	for _, f := range []string{"/p/x.aa", "/p/x.bb"} {
		if !strings.Contains(m.UnavailableReason(f), "bridge cycle") {
			t.Fatalf("cycle member not disabled for %s: %q", f, m.UnavailableReason(f))
		}
	}

	// A chain merely reaching a cycle disables too; the valid vue→typescript
	// shape does not.
	m = NewManager(config.Config{Languages: map[string]config.LanguageConfig{
		"vue":        {Extensions: []string{".vue"}, LSP: mk("typescript")},
		"typescript": {Extensions: []string{".ts"}, LSP: &config.LSPServerConfig{Command: "x"}},
	}}, t.TempDir())
	if r := m.UnavailableReason("/p/a.vue"); r != "" {
		t.Fatalf("valid bridge chain wrongly disabled: %q", r)
	}
}
