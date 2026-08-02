package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/store"
)

// coldModel builds a model straight from New() — cold cache, splash active —
// bypassing newTestModel's warm-up.
func coldModel(t *testing.T) Model {
	t.Helper()
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))
	m := New(config.Default(), store.NewMemory(), root, "", nil)
	m.width, m.height, m.ready = 100, 30, true
	return m
}

func TestColdStartShowsSplash(t *testing.T) {
	m := coldModel(t)
	if !m.splash || !m.corpusRebuilding {
		t.Fatalf("cold start: splash=%v rebuilding=%v, want both true", m.splash, m.corpusRebuilding)
	}
	out := ansi.Strip(m.render())
	for _, want := range []string{"ntee-editor", versionTag(), "building file tree"} {
		if !strings.Contains(out, want) {
			t.Fatalf("splash missing %q:\n%s", want, out)
		}
	}
}

func TestSplashDismissesAfterCorpusAndMinDisplay(t *testing.T) {
	m := coldModel(t)
	m = rebuildCorpusNow(m) // index lands
	if !m.splash {
		t.Fatal("corpusMsg alone must not dismiss the splash (the tick does)")
	}

	// Minimum display not yet elapsed: the tick keeps the splash and re-arms.
	m.splashStart = time.Now()
	next, cmd := m.Update(splashTickMsg{})
	m = next.(Model)
	if !m.splash || cmd == nil {
		t.Fatalf("fresh splash: splash=%v cmd=%v, want held and re-armed", m.splash, cmd)
	}

	// Minimum display elapsed: the tick dismisses and ends the chain.
	m.splashStart = time.Now().Add(-time.Second)
	next, cmd = m.Update(splashTickMsg{})
	m = next.(Model)
	if m.splash || cmd != nil {
		t.Fatalf("elapsed splash: splash=%v cmd=%v, want dismissed with no re-arm", m.splash, cmd)
	}

	// A dead tick after dismissal is a no-op.
	next, cmd = m.Update(splashTickMsg{})
	if next.(Model).splash || cmd != nil {
		t.Fatal("dead tick must be dropped")
	}
}

func TestSplashAnyKeySkipsAndIsConsumed(t *testing.T) {
	m := coldModel(t)
	m = key(m, typeRune('x'))
	if m.splash {
		t.Fatal("a key must skip the splash")
	}
	if m.command != "" {
		t.Fatalf("the skipping keystroke leaked into the query bar: %q", m.command)
	}
}

func TestSplashCtrlCStillQuits(t *testing.T) {
	m := coldModel(t)
	_, cmd := m.Update(ctrlKey('c'))
	if cmd == nil {
		t.Fatal("ctrl+c during splash must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c cmd yielded %T, want tea.QuitMsg", cmd())
	}
}

func TestSplashIgnoresMouseAndPaste(t *testing.T) {
	m := coldModel(t)
	next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft})
	if !next.(Model).splash {
		t.Fatal("mouse must not dismiss the splash")
	}
	next, _ = m.Update(tea.PasteMsg{Content: "hello"})
	m = next.(Model)
	if !m.splash || m.command != "" {
		t.Fatalf("paste during splash: splash=%v command=%q", m.splash, m.command)
	}
}

func TestWarmStartSkipsSplash(t *testing.T) {
	db := store.NewMemory()
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))

	m := New(config.Default(), db, root, "", nil)
	m = rebuildCorpusNow(m) // persists the index to db via the corpusMsg handler

	m2 := New(config.Default(), db, root, "", nil)
	if m2.splash {
		t.Fatal("warm start (persisted index) must not show the splash")
	}
	if m2.corpusBuiltAt.IsZero() {
		t.Fatal("warm start should adopt the persisted corpus")
	}
}

func TestGrepGuardWhileIndexing(t *testing.T) {
	m := coldModel(t)
	m.splash = false // user skipped the splash; index still building
	m, _ = m.openGrep()
	if m.grepOpen {
		t.Fatal("grep must not open over an empty cold corpus")
	}
	if m.errText == "" {
		t.Fatal("expected an index-building notice")
	}
}
