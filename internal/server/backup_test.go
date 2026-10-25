package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JudiLite/ecs-controller/internal/store"
)

func TestBackupEndpointsRequireAuthenticationAndCSRF(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(st, t.TempDir(), "")
	for _, action := range []string{"export_backup", "inspect_backup", "restore_backup", "download_restore_backup"} {
		r := httptest.NewRequest("POST", "/index.php?action="+action, bytes.NewBufferString(`{"password":"valid-password-123"}`))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s anonymous status=%d", action, w.Code)
		}
		_ = st.CreateSession("session", "csrf", time.Hour)
		r = httptest.NewRequest("POST", "/index.php?action="+action, nil)
		r.AddCookie(&http.Cookie{Name: "ecs_session", Value: "session"})
		w = httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s missing CSRF status=%d", action, w.Code)
		}
	}
}

func TestBackupPreviewRestoreAndSafetyCopy(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.SetAdminCredentials("admin", "admin-password-123")
	_ = st.SetSetting("traffic_threshold", "80")
	password := "valid-backup-password"
	raw, err := st.CreateBackup(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetSetting("traffic_threshold", "99")
	_ = st.CreateSession("session", "csrf", time.Hour)
	s := New(st, dir, "")
	request := func(action, confirmation, fingerprint string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, _ := form.CreateFormFile("backup", "test.ecsbak")
		_, _ = file.Write(raw)
		_ = form.WriteField("password", password)
		_ = form.WriteField("confirmation", confirmation)
		_ = form.WriteField("fingerprint", fingerprint)
		_ = form.Close()
		r := httptest.NewRequest("POST", "/index.php?action="+action, &body)
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("X-CSRF-Token", "csrf")
		r.AddCookie(&http.Cookie{Name: "ecs_session", Value: "session"})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	preview := request("inspect_backup", "", "")
	if preview.Code != 200 {
		t.Fatal(preview.Body.String())
	}
	var data struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Fingerprint == "" || st.GetSetting("traffic_threshold", "") != "99" {
		t.Fatal("inspection changed data")
	}
	if w := request("restore_backup", "恢复", "wrong-file"); w.Code != 400 {
		t.Fatalf("mismatched file accepted: %d", w.Code)
	}
	if w := request("restore_backup", "恢复", data.Fingerprint); w.Code != 200 {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	if st.GetSetting("traffic_threshold", "") != "80" {
		t.Fatal("backup not restored")
	}
	if _, ok := st.Session("session"); ok {
		t.Fatal("session not cleared")
	}
	safety, err := os.ReadFile(filepath.Join(dir, "backups", "before-restore.ecsbak"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.DecodeBackup(safety, password)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	for _, row := range b.Tables["settings"].Rows {
		if row[0] == "traffic_threshold" {
			before = row[1].(string)
		}
	}
	if before != "99" {
		t.Fatal("safety copy did not preserve previous data")
	}
	_ = st.CreateSession("new-session", "new-csrf", time.Hour)
	r := httptest.NewRequest("POST", "/index.php?action=download_restore_backup", nil)
	r.Header.Set("X-CSRF-Token", "new-csrf")
	r.AddCookie(&http.Cookie{Name: "ecs_session", Value: "new-session"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	downloaded, _ := io.ReadAll(w.Result().Body)
	if w.Code != 200 || !bytes.Equal(downloaded, safety) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("safety download failed")
	}
	if err := st.SetSetting("notify_tg_token", "ENC1invalid"); err != nil {
		t.Fatal(err)
	}
	raw, err = st.CreateBackup(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`DELETE FROM settings WHERE key='notify_tg_token'`); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession("session", "csrf", time.Hour); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if w := request("restore_backup", "恢复", hex.EncodeToString(digest[:])); w.Code != 400 {
		t.Fatalf("invalid credentials accepted: %d %s", w.Code, w.Body.String())
	}
	preserved, err := os.ReadFile(filepath.Join(dir, "backups", "before-restore.ecsbak"))
	if err != nil || !bytes.Equal(preserved, safety) {
		t.Fatal("invalid restore replaced the previous safety backup")
	}
	if st.GetSetting("traffic_threshold", "") != "80" {
		t.Fatal("invalid restore modified current data")
	}
}
