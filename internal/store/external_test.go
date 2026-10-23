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

func TestExternalRecordBaseIsStoredExplicitly(t *testing.T) {
	history := &ExternalHistory{dir: t.TempDir()}
	at := time.Unix(2000, 0)
	records := []ExternalRecord{
		{Key: NewExternalRecordKey(at), Command: "open", Paths: []string{"a.go"}, ReceivedAt: at},
		{Key: NewExternalRecordKey(at.Add(time.Second)), Command: "review", Args: []string{"v1.2.0"}, Base: "v1.2.0", Paths: []string{"a.go"}, ReceivedAt: at.Add(time.Second)},
	}
	for _, record := range records {
		if err := history.AppendExternal(record, 10); err != nil {
			t.Fatal(err)
		}
	}
	var raw map[string]any
	err := history.withDB(10, func(db *nteedb.DB) error {
		data, _, err := db.Get(records[0].Key)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, &raw)
	})
	if err != nil || raw["base"] != ExternalDefaultBase {
		t.Fatalf("no revision is stored as HEAD: %v %v", raw, err)
	}
	loaded, _ := history.LoadExternal()
	if len(loaded) != 2 || loaded[0].Base != "v1.2.0" || loaded[1].Base != ExternalDefaultBase {
		t.Fatalf("loaded bases: %+v", loaded)
	}
}

func TestLegacyExternalRecordsGetABase(t *testing.T) {
	cases := []struct {
		json string
		want string
	}{
		{`{"key":"msg:1","command":"open","paths":["a.go"]}`, "HEAD"},
		{`{"key":"msg:2","command":"review","args":["main"],"paths":["a.go"]}`, "main"},
		{`{"key":"msg:3","command":"review","args":[],"paths":["a.go"]}`, "HEAD"},
	}
	for _, c := range cases {
		var record ExternalRecord
		if err := json.Unmarshal([]byte(c.json), &record); err != nil {
			t.Fatal(err)
		}
		if got := record.withDefaultBase().Base; got != c.want {
			t.Errorf("%s → base %q, want %q", c.json, got, c.want)
		}
	}
}

func TestExternalPathsShareTheProjectHash(t *testing.T) {
	storeDir, _ := Dir("/some/project")
	externalDir, _ := ExternalDir("/some/project")
	socket, _ := ExternalSocketPath("/some/project")
	if storeDir[len(storeDir)-16:] != externalDir[len(externalDir)-16:] || socket != externalDir+"/ntee.sock" {
		t.Fatalf("store=%s external=%s socket=%s", storeDir, externalDir, socket)
	}
}
