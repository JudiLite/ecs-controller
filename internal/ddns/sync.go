package ddns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/JudiLite/ecs-controller/internal/app"
	"github.com/JudiLite/ecs-controller/internal/notify"
	"github.com/JudiLite/ecs-controller/internal/store"
)

type Result struct {
	GroupID     string `json:"groupId"`
	AccountID   int64  `json:"accountId"`
	Domain      string `json:"domain"`
	Address     string `json:"address"`
	UpdatedAt   int64  `json:"updatedAt"`
	AttemptedAt int64  `json:"attemptedAt"`
	Error       string `json:"error"`
	Key         string `json:"key"`
}

type Updater func(context.Context, notify.DNSUpdateConfig, string) error

func Latest(st *store.Store, groupID string) Result {
	var result Result
	_ = json.Unmarshal([]byte(st.GetSetting("rotation_dns_"+groupID, "{}")), &result)
	return result
}

func Target(group app.RotationGroup, accounts []app.Account, now time.Time) (app.Account, bool) {
	var selected app.Account
	var latest time.Time
	found := false
	for _, member := range group.Members {
		for _, account := range accounts {
			if account.ID != member.AccountID || account.IsDeleted != 0 || account.CloudPresence == "missing" || account.InstanceStatus != "Running" || net.ParseIP(Address(account)).To4() == nil {
				continue
			}
			start := time.Time{}
			if account.ScheduleEnabled && account.ScheduleStartEnabled {
				if clock, err := time.ParseInLocation("15:04", account.StartTime, now.Location()); err == nil {
					for offset := -1; offset <= 1; offset++ {
						day := now.AddDate(0, 0, offset)
						candidate := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location()).Add(-time.Duration(group.AdvanceStartMinutes) * time.Minute)
						if !candidate.After(now) && candidate.After(start) {
							start = candidate
						}
					}
				}
			}
			if !found || start.After(latest) {
				selected, latest, found = account, start, true
			}
		}
	}
	return selected, found
}

func Address(account app.Account) string {
	if account.PublicIPMode == "eip" && account.EIPAddress != "" {
		return strings.TrimSpace(account.EIPAddress)
	}
	return strings.TrimSpace(account.PublicIP)
}

func Sync(ctx context.Context, st *store.Store, group app.RotationGroup, accounts []app.Account, now time.Time, force bool, update Updater) (Result, error) {
	st.RotationDNSMu.Lock()
	defer st.RotationDNSMu.Unlock()
	previous := Latest(st, group.ID)
	target, found := Target(group, accounts, now)
	if !found {
		return previous, fmt.Errorf("分组没有正在运行且具有公网 IPv4 的实例，请先开机并刷新实例状态")
	}
	config := notify.DNSUpdateConfig{Provider: group.Provider, Domain: group.Domain, TTL: group.DNS.TTL, CloudflareToken: group.DNS.CloudflareToken, CloudflareProxied: group.DNS.CloudflareProxied, DNSPodSecretID: group.DNS.DNSPodSecretID, DNSPodSecretKey: group.DNS.DNSPodSecretKey, AliDNSAccessKeyID: group.DNS.AliDNSAccessKeyID, AliDNSAccessKeySecret: group.DNS.AliDNSSecret}
	encoded, _ := json.Marshal(config)
	digest := sha256.Sum256(append(encoded, []byte(Address(target))...))
	key := hex.EncodeToString(digest[:])
	if !force && previous.Key == key && previous.AccountID == target.ID {
		if previous.Error == "" && previous.UpdatedAt > 0 && now.Unix()-previous.UpdatedAt < 6*60*60 {
			return previous, nil
		}
		if previous.Error != "" && now.Unix()-previous.AttemptedAt < 60 {
			return previous, fmt.Errorf("%s", previous.Error)
		}
	}
	result := Result{GroupID: group.ID, AccountID: target.ID, Domain: strings.TrimSpace(group.Domain), Address: Address(target), UpdatedAt: previous.UpdatedAt, AttemptedAt: now.Unix(), Key: key}
	if update == nil {
		update = notify.UpdateDNSRecord
	}
	err := update(ctx, config, result.Address)
	if err != nil {
		result.Error = err.Error()
	} else {
		result.UpdatedAt = now.Unix()
	}
	raw, _ := json.Marshal(result)
	if saveErr := st.SetSetting("rotation_dns_"+group.ID, string(raw)); saveErr != nil {
		return result, fmt.Errorf("保存 DDNS 同步结果失败: %w", saveErr)
	}
	return result, err
}
