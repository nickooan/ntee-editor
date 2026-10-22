package store

import (
	"encoding/json"
	"testing"
	"time"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

func TestGlobalExternalSettingsDefaultsAndExplicitWrite(t *testing.T) {
	dir := t.TempDir()
	settingsStore := NewGlobalExternalSettings(dir)
	settings, err := settingsStore.LoadExternalSettings()
	if err != nil || settings != DefaultExternalSettings {
		t.Fatalf("a missing record loads the defaults (off, 10): %+v %v", settings, err)
	}
	if err := settingsStore.SaveExternalSettings(ExternalSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Both fields are stored explicitly, the unset limit as its default.
	var raw map[string]any
	err = withDBAt(nteedb.Options{Dir: dir}, func(db *nteedb.DB) error {
		data, _, err := db.Get(externalSettingsKey)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, &raw)
	})
	if err != nil || raw["enabled"] != true || raw["historyLimit"] != float64(DefaultExternalHistoryLimit) {
		t.Fatalf("stored record = %v %v", raw, err)
	}
	if err := settingsStore.SaveExternalSettings(ExternalSettings{Enabled: false, HistoryLimit: 500}); err != nil {
		t.Fatal(err)
	}
	if settings, _ = settingsStore.LoadExternalSettings(); settings.HistoryLimit != MaxExternalHistoryLimit {
		t.Fatalf("limit clamps to the maximum: %d", settings.HistoryLimit)
	}
}

func appendRecords(t *testing.T, history ExternalHistoryStore, count, limit int) []string {
	t.Helper()
	keys := make([]string, count)
	for i := range count {
		record := ExternalRecord{
			Key:        NewExternalRecordKey(time.Unix(int64(1000+i), 0)),
			Command:    "open",
			Paths:      []string{"main.go"},
			ReceivedAt: time.Unix(int64(1000+i), 0),
			Status:     ExternalPending,
		}
		keys[i] = record.Key
		if err := history.AppendExternal(record, limit); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func exerciseExternalHistory(t *testing.T, history ExternalHistoryStore) {
	t.Helper()
	keys := appendRecords(t, history, 5, 3)
	records, err := history.LoadExternal()
	if err != nil || len(records) != 3 {
		t.Fatalf("the cap keeps the latest 3: %d %v", len(records), err)
	}
	if records[0].Key != keys[4] || records[2].Key != keys[2] {
		t.Fatalf("newest first, oldest evicted: %s … %s", records[0].Key, records[2].Key)
	}

	if err := history.UpdateExternalStatus(keys[4], ExternalOpened, "done"); err != nil {
		t.Fatal(err)
	}
	records, _ = history.LoadExternal()
	if records[0].Status != ExternalOpened || records[0].Note != "done" || len(records) != 3 {
		t.Fatalf("status update: %+v (%d records)", records[0], len(records))
	}

	if err := history.TrimExternal(1); err != nil {
		t.Fatal(err)
	}
	records, _ = history.LoadExternal()
	if len(records) != 1 || records[0].Key != keys[4] {
		t.Fatalf("a lowered limit trims to the newest at once: %+v", records)
	}
}

func TestExternalHistoryCapsWithNteeDB(t *testing.T) {
	exerciseExternalHistory(t, &ExternalHistory{dir: t.TempDir()})
}

func TestMemoryExternalHistory(t *testing.T) {
	exerciseExternalHistory(t, NewMemoryExternalHistory())
}

func TestExternalPathsShareTheProjectHash(t *testing.T) {
	storeDir, _ := Dir("/some/project")
	externalDir, _ := ExternalDir("/some/project")
	socket, _ := ExternalSocketPath("/some/project")
	if storeDir[len(storeDir)-16:] != externalDir[len(externalDir)-16:] || socket != externalDir+"/ntee.sock" {
		t.Fatalf("store=%s external=%s socket=%s", storeDir, externalDir, socket)
	}
}
