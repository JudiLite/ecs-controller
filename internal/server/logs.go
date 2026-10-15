package server

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Kori1c/ecs-controller/internal/store"
)

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	limit := 50
	if value := values.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			s.error(w, http.StatusBadRequest, "每页日志数量必须在 1 到 100 之间")
			return
		}
		limit = parsed
	}
	var before int64
	if value := values.Get("before_id"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			s.error(w, http.StatusBadRequest, "日志分页位置无效")
			return
		}
		before = parsed
	}
	page, err := s.Store.QueryLogs(r.Context(), store.LogQuery{Tab: values.Get("tab"), Level: values.Get("level"), Search: values.Get("q"), BeforeID: before, Limit: limit})
	if err != nil {
		if errors.Is(err, store.ErrInvalidLogQuery) {
			s.error(w, http.StatusBadRequest, err.Error())
		} else {
			s.error(w, http.StatusInternalServerError, "日志读取失败，请稍后重试")
		}
		return
	}
	interval, err := strconv.Atoi(os.Getenv("ECS_MONITOR_INTERVAL"))
	if err != nil || interval < 1 {
		interval = 60
	}
	if interval < 30 {
		interval = 30
	}
	s.json(w, http.StatusOK, struct {
		store.LogPage
		ServerTime      int64 `json:"server_time"`
		MonitorInterval int   `json:"monitor_interval_seconds"`
	}{page, time.Now().Unix(), interval})
}

func (s *Server) clearLogs(w http.ResponseWriter, data map[string]any) {
	tab := stringValue(data["tab"])
	if tab != "action" && tab != "heartbeat" {
		s.error(w, http.StatusBadRequest, "请选择要清空的日志类别")
		return
	}
	if err := s.Store.ClearLogs(tab); err != nil {
		s.error(w, http.StatusInternalServerError, "日志清空失败，请稍后重试")
		return
	}
	s.json(w, http.StatusOK, map[string]any{"success": true})
}
