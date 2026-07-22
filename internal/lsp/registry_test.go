package lsp

import (
	"strings"
	"testing"

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
