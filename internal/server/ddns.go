package server

import (
	"net/http"
	"time"

	"github.com/JudiLite/ecs-controller/internal/ddns"
)

func (s *Server) syncRotationDNS(w http.ResponseWriter, r *http.Request, data map[string]any) {
	groups, err := s.Store.LoadRotationGroups()
	if err != nil {
		s.error(w, 500, "读取分组失败")
		return
	}
	for _, group := range groups {
		if group.ID != stringValue(data["groupId"]) {
			continue
		}
		accounts, err := s.Store.LoadAccounts(false)
		if err != nil {
			s.error(w, 500, "读取实例失败")
			return
		}
		result, err := ddns.Sync(r.Context(), s.Store, group, accounts, time.Now(), true, s.DNSUpdater)
		if err != nil {
			s.error(w, 400, "DDNS 同步失败: "+err.Error())
			return
		}
		s.json(w, 200, map[string]any{"success": true, "result": result, "message": "域名解析已同步至 " + result.Address})
		return
	}
	s.error(w, 404, "轮转分组不存在")
}

func (s *Server) rotationDNSStatus(w http.ResponseWriter) {
	groups, err := s.Store.LoadRotationGroups()
	if err != nil {
		s.error(w, 500, "读取分组失败")
		return
	}
	result := map[string]ddns.Result{}
	for _, group := range groups {
		result[group.ID] = ddns.Latest(s.Store, group.ID)
	}
	s.json(w, 200, map[string]any{"data": result})
}
