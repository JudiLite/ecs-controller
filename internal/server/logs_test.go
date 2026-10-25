package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JudiLite/ecs-controller/internal/store"
)

func logsTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateSession("logs-session", "logs-csrf", time.Hour); err != nil {
		t.Fatal(err)
	}
	return New(st, dir, "unused"), st
}

func logsRequest(method, target, body string, csrf bool) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "ecs_session", Value: "logs-session"})
	if csrf {
		r.Header.Set("X-CSRF-Token", "logs-csrf")
	}
	return r
}

func TestLogsEndpointFiltersAndReturnsPageMetadata(t *testing.T) {
	srv, st := logsTestServer(t)
	for i := 0; i < 60; i++ {
		st.AddLog("info", "routine")
	}
	st.AddLog("error", "target failure")
	st.AddLog("heartbeat", "loop response")
	t.Setenv("ECS_MONITOR_INTERVAL", "10")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, logsRequest(http.MethodGet, "/index.php?action=get_logs&tab=action&level=error&q=target&limit=1", "", false))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		store.LogPage
		ServerTime int64 `json:"server_time"`
		Interval   int   `json:"monitor_interval_seconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].Type != "error" || body.LastHeartbeat == nil || body.LatestID != body.Data[0].ID || body.Interval != 30 || body.ServerTime <= 0 {
		t.Fatalf("bad response: %+v", body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("log response must not be cached")
	}
}

func TestLogsEndpointRejectsBadParametersAndDoesNotHideDatabaseErrors(t *testing.T) {
	srv, st := logsTestServer(t)
	for _, suffix := range []string{"&tab=unknown", "&level=unknown", "&tab=heartbeat&level=error", "&limit=0", "&limit=101", "&limit=bad", "&before_id=-1", "&before_id=bad", "&before_id=999999999999999999999999"} {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, logsRequest(http.MethodGet, "/index.php?action=get_logs"+suffix, "", false))
		if w.Code != 400 {
			t.Fatalf("%s status=%d", suffix, w.Code)
		}
	}
	st.Close()
	w := httptest.NewRecorder()
	srv.getLogs(w, httptest.NewRequest(http.MethodGet, "/?tab=action", nil))
	if w.Code != 500 || strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("DB error was hidden: %d %s", w.Code, w.Body.String())
	}
}

func TestLogsEndpointRequiresLoginAndClearRequiresCSRF(t *testing.T) {
	srv, st := logsTestServer(t)
	st.AddLog("info", "action remains")
	st.AddLog("heartbeat", "heartbeat remains")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/index.php?action=get_logs", nil))
	if w.Code != 403 {
		t.Fatalf("anonymous read status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, logsRequest(http.MethodPost, "/index.php?action=clear_logs", `{"tab":"action"}`, false))
	if w.Code != 403 || len(st.Logs("action", 20)) != 1 {
		t.Fatal("clear did not require CSRF")
	}
	for _, body := range []string{`{}`, `{"tab":"unknown"}`, `broken JSON`} {
		w = httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, logsRequest(http.MethodPost, "/index.php?action=clear_logs", body, true))
		if w.Code != 400 || len(st.Logs("action", 20)) != 1 {
			t.Fatalf("bad clear deleted data: %d", w.Code)
		}
	}
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, logsRequest(http.MethodPost, "/index.php?action=clear_logs", `{"tab":"heartbeat"}`, true))
	if w.Code != 200 || len(st.Logs("heartbeat", 20)) != 0 || len(st.Logs("action", 20)) != 1 {
		t.Fatal("clear category isolation failed")
	}
}
