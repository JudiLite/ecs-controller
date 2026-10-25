package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func logTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestQueryLogsFiltersAcrossPagesAndRetainsHeartbeatMetadata(t *testing.T) {
	st := logTestStore(t)
	st.AddLog("error", "older matching failure")
	for i := 0; i < 80; i++ {
		st.AddLog("info", fmt.Sprintf("routine %d", i))
	}
	st.AddLog("warning", "warning only")
	st.AddLog("heartbeat", "monitor loop response")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	page, err := st.QueryLogs(ctx, LogQuery{Level: "error", Search: "matching"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].Message != "older matching failure" || page.LastHeartbeat == nil || page.LastHeartbeat.Type != "heartbeat" {
		t.Fatalf("unexpected filtered page: %+v", page)
	}
	if page.HasMore || page.LatestID != page.Data[0].ID {
		t.Fatalf("bad metadata: %+v", page)
	}
	page, err = st.QueryLogs(ctx, LogQuery{Tab: "heartbeat"})
	if err != nil || len(page.Data) != 1 || page.Data[0].Type != "heartbeat" {
		t.Fatalf("heartbeat isolation: %+v %v", page, err)
	}
}

func TestQueryLogsCursorDoesNotDuplicateOrSkipAfterNewInserts(t *testing.T) {
	st := logTestStore(t)
	for i := 0; i < 123; i++ {
		st.AddLog("info", fmt.Sprintf("entry %d", i))
	}
	ctx := context.Background()
	first, err := st.QueryLogs(ctx, LogQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Data) != 50 || !first.HasMore {
		t.Fatalf("first page: %+v", first)
	}
	initialLatest := first.LatestID
	st.AddLog("error", "new entry while reading old pages")
	seen := map[int64]bool{}
	page := first
	for {
		for _, record := range page.Data {
			if seen[record.ID] || record.ID > initialLatest {
				t.Fatalf("duplicate or new record in older pages: %d", record.ID)
			}
			seen[record.ID] = true
		}
		if !page.HasMore {
			break
		}
		previous := page.NextBeforeID
		page, err = st.QueryLogs(ctx, LogQuery{Limit: 50, BeforeID: previous})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Data) == 0 || page.Data[0].ID >= previous {
			t.Fatal("cursor did not advance")
		}
	}
	if len(seen) != 123 {
		t.Fatalf("loaded %d records, want 123", len(seen))
	}
	latest, err := st.QueryLogs(ctx, LogQuery{Limit: 1})
	if err != nil || latest.LatestID <= initialLatest || latest.Data[0].Message != "new entry while reading old pages" {
		t.Fatalf("new entry not detectable: %+v %v", latest, err)
	}
}

func TestQueryLogsTreatsSearchCharactersLiterally(t *testing.T) {
	st := logTestStore(t)
	for _, message := range []string{"percent 95% complete", "value a_b", `path C:\logs`, "normal text", "mixed CASE marker"} {
		st.AddLog("info", message)
	}
	for _, query := range []string{"%", "_", `\`, "case MARKER"} {
		page, err := st.QueryLogs(context.Background(), LogQuery{Search: query})
		if err != nil || len(page.Data) != 1 {
			t.Fatalf("literal query %q: %+v %v", query, page, err)
		}
	}
	page, err := st.QueryLogs(context.Background(), LogQuery{Search: `' OR 1=1 --`})
	if err != nil || len(page.Data) != 0 {
		t.Fatalf("injection changed matching: %+v %v", page, err)
	}
}

func TestQueryLogsValidationAndEmptyPage(t *testing.T) {
	st := logTestStore(t)
	for _, query := range []LogQuery{{Tab: "invalid"}, {Level: "invalid"}, {Tab: "heartbeat", Level: "error"}, {BeforeID: -1}, {Limit: -1}, {Limit: 101}, {Search: strings.Repeat("字", 201)}} {
		if _, err := st.QueryLogs(context.Background(), query); !errors.Is(err, ErrInvalidLogQuery) {
			t.Fatalf("expected validation error for %+v, got %v", query, err)
		}
	}
	page, err := st.QueryLogs(context.Background(), LogQuery{})
	if err != nil || page.Data == nil || len(page.Data) != 0 || page.LastHeartbeat != nil || page.HasMore || page.LatestID != 0 {
		t.Fatalf("empty page: %+v %v", page, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.QueryLogs(ctx, LogQuery{}); err == nil {
		t.Fatal("canceled request ignored")
	}
}

func TestClearLogsIsolatesCategoriesAndRejectsUnknownTab(t *testing.T) {
	st := logTestStore(t)
	st.AddLog("info", "action")
	st.AddLog("heartbeat", "heartbeat")
	if err := st.ClearLogs("unknown"); err == nil {
		t.Fatal("invalid category accepted")
	}
	if len(st.Logs("action", 20)) != 1 || len(st.Logs("heartbeat", 20)) != 1 {
		t.Fatal("invalid clear deleted logs")
	}
	if err := st.ClearLogs("heartbeat"); err != nil {
		t.Fatal(err)
	}
	if len(st.Logs("action", 20)) != 1 || len(st.Logs("heartbeat", 20)) != 0 {
		t.Fatal("heartbeat clear affected actions")
	}
	st.AddLog("heartbeat", "new heartbeat")
	if err := st.ClearLogs("action"); err != nil {
		t.Fatal(err)
	}
	if len(st.Logs("action", 20)) != 0 || len(st.Logs("heartbeat", 20)) != 1 {
		t.Fatal("action clear affected heartbeat")
	}
}
