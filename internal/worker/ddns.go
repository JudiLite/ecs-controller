package worker

import (
	"context"
	"time"

	"github.com/JudiLite/ecs-controller/internal/app"
	"github.com/JudiLite/ecs-controller/internal/ddns"
)

func (w *Worker) syncRotationDNS(ctx context.Context, account app.Account, now time.Time) {
	group, _ := w.rotationGroupForAccount(account.ID)
	if group == nil || group.Domain == "" {
		return
	}
	accounts, err := w.Store.LoadAccounts(false)
	if err != nil {
		return
	}
	for index := range accounts {
		if accounts[index].ID == account.ID {
			accounts[index] = account
		}
	}
	target, found := ddns.Target(*group, accounts, now)
	if !found || target.ID != account.ID {
		return
	}
	previous := ddns.Latest(w.Store, group.ID)
	result, syncErr := ddns.Sync(ctx, w.Store, *group, accounts, now, false, w.DNSUpdater)
	if result.AttemptedAt == previous.AttemptedAt {
		return
	}
	if syncErr != nil {
		w.Store.AddLog("warning", "分组 DDNS 同步失败: "+syncErr.Error())
	} else {
		w.Store.AddLog("info", "分组 ["+group.Name+"] DDNS 已切换 "+result.Domain+" -> "+result.Address)
	}
	w.audit(app.AuditSourceRotation, "ddns_update", "rotation_group", group.ID, "更新分组域名解析", syncErr)
}
