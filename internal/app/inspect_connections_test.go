package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/staging"
	"github.com/nickooan/ntee-editor/internal/store"
)

// reportedConnections is the connection status a fake store reports.
type reportedConnections struct{ statuses []store.ConnectionStatus }

func (r *reportedConnections) Connections() []store.ConnectionStatus { return r.statuses }

type reportingBackend struct {
	*store.Memory
	*reportedConnections
}

type reportingEditLog struct {
	*store.MemoryAgentEditLog
	*reportedConnections
}

type reportingHistory struct {
	*store.MemoryExternalHistory
	*reportedConnections
}

// connectionsModel is a model whose project store, edit log and history
// report the given connections; its settings store is in memory.
func connectionsModel(t *testing.T, project, edits, history store.ConnectionStatus) (Model, *reportedConnections) {
	t.Helper()
	m, root := newTestModel(t, reportingBackend{store.NewMemory(), &reportedConnections{[]store.ConnectionStatus{project}}})
	editsReport := &reportedConnections{[]store.ConnectionStatus{edits}}
	m = m.WithAgentEdits(staging.NewRegistry(root, reportingEditLog{store.NewMemoryAgentEditLog(), editsReport}))
	m = m.WithExternal(store.NewMemoryExternalSettings(store.DefaultExternalSettings),
		reportingHistory{store.NewMemoryExternalHistory(), &reportedConnections{[]store.ConnectionStatus{history}}}, "", "")
	m.mode, m.inspectMenu = modeInspect, inspectMenuDB
	return m, editsReport
}

func TestTheDBPaneShowsEveryConnection(t *testing.T) {
	now := time.Now()
	m, _ := connectionsModel(t,
		store.ConnectionStatus{Name: "project store", Mode: store.ConnectionSessionLong, Open: true, Opens: 1, OpenedAt: now.Add(-time.Hour)},
		store.ConnectionStatus{Name: "agent edits", Mode: store.ConnectionSessionLease, Open: true, Sessions: 2, Opens: 1, Calls: 48, Reconnects: 1,
			OpenedAt: now.Add(-72 * time.Second), LastUsed: now.Add(-4 * time.Second), ClosesAt: now.Add(26 * time.Second)},
		store.ConnectionStatus{Name: "message history", Mode: store.ConnectionSessionLease, Opens: 9, Calls: 212, LastUsed: now.Add(-3 * time.Minute), LastError: "disk full"},
	)
	m.inspectLoading = false
	pane := ansi.Strip(m.renderInspectDB(120, 80))
	for _, want := range []string{
		"connections", "CONNECTION", "STATE",
		"project store", "● open  session-long · 1h00m since start",
		"agent edits", "● open  2 sessions · 1m12s open · 4s idle · closes in 26s",
		"reuse █████████░  48 calls on 1 open · 1 reconnect",
		"message history", "○ closed  last used 3m00s ago", "212 calls on 9 opens · 0 reconnects", "✗ disk full",
		"global", "◌ in memory",
	} {
		if !strings.Contains(pane, want) {
			t.Fatalf("the ntee-db pane shows %q:\n%s", want, pane)
		}
	}
}

func TestTheDBPaneRefreshesOnlyWhileALeaseIsOpen(t *testing.T) {
	open := store.ConnectionStatus{Name: "agent edits", Mode: store.ConnectionSessionLease, Open: true, Sessions: 1, Opens: 1, Calls: 1}
	m, edits := connectionsModel(t, store.ConnectionStatus{Name: "project store", Mode: store.ConnectionSessionLong, Open: true}, open,
		store.ConnectionStatus{Name: "message history", Mode: store.ConnectionSessionLease})

	next, cmd := m.Update(inspectConnectionsTickMsg{})
	m = next.(Model)
	if cmd == nil || !m.inspectConnectionsTicking {
		t.Fatal("an open lease keeps the pane counting")
	}
	if _, again := m.Update(inspectConnectionsTickMsg{}); again == nil {
		t.Fatal("each tick arms the next while the lease is open")
	}

	m.inspectMenu = inspectMenuExternal
	if next, cmd = m.Update(inspectConnectionsTickMsg{}); cmd != nil || next.(Model).inspectConnectionsTicking {
		t.Fatal("another pane stops the refresh")
	}

	m.inspectMenu = inspectMenuDB
	closed := open
	closed.Open = false
	edits.statuses = []store.ConnectionStatus{closed}
	if next, cmd = m.Update(inspectConnectionsTickMsg{}); cmd != nil || next.(Model).inspectConnectionsTicking {
		t.Fatal("nothing open, nothing to count: the refresh stops")
	}
}

func TestCompactDuration(t *testing.T) {
	for duration, want := range map[time.Duration]string{
		0: "0s", 4 * time.Second: "4s", 72 * time.Second: "1m12s", 2*time.Hour + 5*time.Minute: "2h05m",
	} {
		if got := compactDuration(duration); got != want {
			t.Fatalf("compactDuration(%v) = %q, want %q", duration, got, want)
		}
	}
}

func TestTheDBPaneWithConnectionsFitsItsHeight(t *testing.T) {
	m := inspectPanelModel(t, inspectMenuDB)
	for _, height := range []int{40, 20, 12} {
		if lines := strings.Count(ansi.Strip(m.renderInspectMain(80, height)), "\n") + 1; lines > height {
			t.Fatalf("height %d: the pane drew %d lines", height, lines)
		}
	}
	if out := ansi.Strip(m.renderInspectMain(80, 40)); !strings.Contains(out, "Commands") || !strings.Contains(out, "connections") {
		t.Fatalf("with room, both tables and the legend show:\n%s", out)
	}
}

func TestConnectionRowsPaintEveryGapInThePanelBackground(t *testing.T) {
	now := time.Now()
	for _, status := range []store.ConnectionStatus{
		{Name: "project store", Mode: store.ConnectionSessionLong, Open: true, OpenedAt: now},
		{Name: "agent edits", Mode: store.ConnectionSessionLease, Open: true, Sessions: 1, Opens: 1, Calls: 3, OpenedAt: now, LastUsed: now, ClosesAt: now.Add(time.Second)},
		{Name: "message history", Mode: store.ConnectionSessionLease, Opens: 2, Calls: 5, LastUsed: now, LastError: "boom"},
		{Name: "global", Mode: store.ConnectionPerCall, Opens: 1, Calls: 1, LastUsed: now, Note: "settings cache fresh"},
		{Name: "global"},
	} {
		for _, piece := range connectionPieces(status, now) {
			for _, run := range strings.SplitAfter(piece, "\x1b[m") {
				if strings.HasPrefix(run, " ") {
					t.Fatalf("%s: a gap outside any style shows the terminal background: %q", status.Name, piece)
				}
			}
		}
	}
}
