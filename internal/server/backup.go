package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/JudiLite/ecs-controller/internal/app"
	"github.com/JudiLite/ecs-controller/internal/store"
)

func (s *Server) backupAction(w http.ResponseWriter, r *http.Request, action string) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		s.error(w, 405, "请使用 POST 请求")
		return
	}
	if action == "download_restore_backup" {
		raw, err := os.ReadFile(filepath.Join(s.DataDir, "backups", "before-restore.ecsbak"))
		if err != nil {
			s.error(w, 404, "暂无恢复前备份")
			return
		}
		s.sendBackup(w, raw, "ecs-before-restore.ecsbak")
		return
	}
	if action == "export_backup" {
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		data, err := readJSON(r)
		if err != nil {
			s.error(w, 400, "请求无效")
			return
		}
		raw, err := s.Store.CreateBackup(r.Context(), stringValue(data["password"]))
		if err != nil {
			s.error(w, 400, err.Error())
			return
		}
		s.Store.AddLog("info", "已导出密码加密备份")
		s.sendBackup(w, raw, "ecs-backup-"+time.Now().Format("20060102-150405")+".ecsbak")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxBackupBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		s.error(w, 400, "备份文件无效或超过 32 MB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, _, err := r.FormFile("backup")
	if err != nil {
		s.error(w, 400, "请选择备份文件")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, store.MaxBackupBytes+1))
	if err != nil {
		s.error(w, 400, "无法读取备份")
		return
	}
	password := r.FormValue("password")
	b, err := store.DecodeBackup(raw, password)
	if err != nil {
		s.error(w, 400, err.Error())
		return
	}
	digest := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(digest[:])
	if action == "inspect_backup" {
		if err := s.Store.ValidateBackup(r.Context(), b); err != nil {
			s.error(w, 400, err.Error())
			return
		}
		s.json(w, 200, map[string]any{"summary": b.Summary(), "fingerprint": fingerprint})
		return
	}
	if r.FormValue("confirmation") != "恢复" || r.FormValue("fingerprint") != fingerprint {
		s.error(w, 400, "请先校验备份并输入“恢复”确认")
		return
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !s.Store.Operations.TryLock() {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			s.error(w, 409, "当前有操作正在执行，请稍后再恢复")
			return
		case <-ticker.C:
		}
	}
	defer s.Store.Operations.Unlock()
	// Re-check the session after waiting for exclusive access.
	if !s.authenticated(r) {
		s.error(w, 403, "登录已过期，请重新登录")
		return
	}
	if err := s.Store.ValidateBackup(r.Context(), b); err != nil {
		s.error(w, 400, err.Error())
		return
	}
	var active int
	if err := s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running','retry')`).Scan(&active); err != nil {
		s.error(w, 500, "无法检查后台任务")
		return
	}
	if active > 0 {
		s.error(w, 409, "仍有待执行或重试中的后台任务，请完成后再恢复")
		return
	}
	safety, err := s.Store.CreateBackup(r.Context(), password)
	if err != nil {
		s.error(w, 500, "无法创建恢复前备份，已取消恢复")
		return
	}
	dir := filepath.Join(s.DataDir, "backups")
	if err = os.MkdirAll(dir, 0700); err != nil {
		s.error(w, 500, "无法保存恢复前备份，已取消恢复")
		return
	}
	tmp, err := os.CreateTemp(dir, ".before-restore-*")
	if err != nil {
		s.error(w, 500, "无法保存恢复前备份，已取消恢复")
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, err = tmp.Write(safety)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(dir, "before-restore.ecsbak"))
	}
	if err != nil {
		s.error(w, 500, "无法保存恢复前备份，已取消恢复")
		return
	}
	if err = s.Store.RestoreBackup(r.Context(), b); err != nil {
		s.error(w, 400, "恢复已取消，原数据保持不变："+err.Error())
		return
	}
	s.mu.Lock()
	s.previews = map[string]map[string]any{}
	s.mu.Unlock()
	s.Store.AddLog("info", "已恢复加密备份；恢复前数据已保存至 backups/before-restore.ecsbak")
	_ = s.Store.AddAudit(app.AuditEntry{RequestID: app.NewAuditRequestID("web"), Source: app.AuditSourceWeb, Action: "backup_restore", EntityType: "configuration", Summary: "恢复加密备份", Success: true})
	http.SetCookie(w, &http.Cookie{Name: "ecs_session", MaxAge: -1, Path: "/", HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode})
	s.json(w, 200, map[string]any{"success": true, "message": "恢复完成，请使用当前服务器原有管理员账号重新登录"})
}

func (s *Server) sendBackup(w http.ResponseWriter, raw []byte, filename string) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
