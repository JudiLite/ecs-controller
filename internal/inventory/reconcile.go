package inventory

import (
	"context"
	"errors"
	"fmt"
	"github.com/JudiLite/ecs-controller/internal/app"
	"github.com/JudiLite/ecs-controller/internal/cloud"
	"github.com/JudiLite/ecs-controller/internal/store"
	"strconv"
	"time"
)

type Result struct {
	RemoteCount int
	Purged      int
	Recovered   int
}

func SyncGroup(ctx context.Context, st *store.Store, group app.AccountGroup, client cloud.Client, now time.Time, logf func(string, ...any), background ...bool) (Result, error) {
	result := Result{}
	confirmMissing := len(background) > 0 && background[0]
	instances, err := client.DescribeInstances(ctx, group.RegionID)
	if err != nil {
		return result, err
	}
	accounts, err := st.LoadAccounts(true)
	if err != nil {
		return result, err
	}
	publicNetworks := map[string]cloud.InstancePublicNetwork{}
	publicNetworkSynced := false
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if networkClient, ok := client.(cloud.InstancePublicNetworkClient); ok {
		instanceIDs := make([]string, 0, len(instances))
		for _, instance := range instances {
			instanceIDs = append(instanceIDs, instance.ID)
		}
		if networks, networkErr := networkClient.DescribeInstancePublicNetworks(ctx, group.RegionID, instanceIDs); networkErr != nil {
			logf("同步实例公网带宽失败（账号组 %s）: %v", group.GroupKey, networkErr)
		} else {
			publicNetworks = networks
			publicNetworkSynced = true
		}
	}
	remoteIDs := make(map[string]bool, len(instances))

	for _, instance := range instances {
		if instance.ID == "" {
			continue
		}
		remoteIDs[instance.ID] = true
		result.RemoteCount++
		var existing *app.Account
		for i := range accounts {
			if !ownedByGroup(accounts[i], group) || accounts[i].InstanceID != instance.ID {
				continue
			}
			if existing == nil || accounts[i].IsDeleted == 0 {
				existing = &accounts[i]
				break
			}
		}
		if existing != nil {
			_ = st.SetSetting(missingKeyForAccount(existing.ID), "0")
		}
		if existing != nil && existing.InstanceStatus == "Releasing" {
			continue
		}
		if existing != nil && existing.IsDeleted != 0 && confirmMissing {
			continue
		}
		a := app.Account{AccessKeyID: group.AccessKeyID, AccessKeySecret: group.AccessKeySecret, RegionID: group.RegionID, InstanceID: instance.ID, MaxTraffic: group.MaxTraffic, Remark: group.Remark, SiteType: group.SiteType, GroupKey: group.GroupKey, InstanceName: instance.Name, InstanceType: instance.InstanceType, InternetBandwidth: instance.InternetBandwidth, PublicIP: instance.PublicIP, PublicIPMode: "ecs_public_ip", PrivateIP: instance.PrivateIP, CPU: instance.CPU, Memory: instance.Memory, OSName: instance.OSName, InstanceStatus: instance.Status, HealthStatus: "ok", UpdatedAt: now.Unix()}
		a.LastSeenAt, a.CloudPresence = now.Unix(), "present"
		if network, hasEIP := publicNetworks[instance.ID]; hasEIP {
			a.PublicIPMode = "eip"
			a.EIPAllocationID, a.EIPAddress = network.AllocationID, network.Address
			if network.Address != "" {
				a.PublicIP = network.Address
			}
			if network.Bandwidth > 0 {
				a.InternetBandwidth = network.Bandwidth
			}
		}
		if existing != nil {
			a.ID = existing.ID
			a.TrafficUsed, a.TrafficBillingMonth = existing.TrafficUsed, existing.TrafficBillingMonth
			a.LastKeepAliveAt, a.AutoStartBlocked = existing.LastKeepAliveAt, existing.AutoStartBlocked
			a.ScheduleLastStartDate, a.ScheduleLastStopDate = existing.ScheduleLastStartDate, existing.ScheduleLastStopDate
			a.ScheduleStopActive, a.ScheduleBlockedByTraffic = existing.ScheduleStopActive, existing.ScheduleBlockedByTraffic
			a.ScheduleEnabled, a.ScheduleStartEnabled, a.ScheduleStopEnabled = existing.ScheduleEnabled, existing.ScheduleStartEnabled, existing.ScheduleStopEnabled
			a.StartTime, a.StopTime = existing.StartTime, existing.StopTime
			a.StoppedMode = existing.StoppedMode
			a.TrafficAPIStatus, a.TrafficAPIMessage = existing.TrafficAPIStatus, existing.TrafficAPIMessage
			a.ProtectionSuspended, a.ProtectionSuspendReason, a.ProtectionNotifiedAt = existing.ProtectionSuspended, existing.ProtectionSuspendReason, existing.ProtectionNotifiedAt
			if network, hasEIP := publicNetworks[instance.ID]; hasEIP {
				a.EIPManaged = existing.EIPManaged && existing.EIPAllocationID == network.AllocationID
			} else if !publicNetworkSynced {
				a.EIPAllocationID, a.EIPAddress, a.EIPManaged = existing.EIPAllocationID, existing.EIPAddress, existing.EIPManaged
				a.PublicIPMode = existing.PublicIPMode
				if a.InternetBandwidth < 1 {
					a.InternetBandwidth = existing.InternetBandwidth
				}
			}
			if a.PublicIPMode == "eip" && a.EIPAddress != "" {
				a.PublicIP = a.EIPAddress
			}
		}
		if err := st.UpsertAccount(a); err != nil {
			return result, err
		}
		if existing != nil && existing.IsDeleted != 0 {
			result.Recovered++
		}
	}
	var failures []error
	for _, account := range accounts {
		if account.InstanceID == "" || account.IsDeleted != 0 || remoteIDs[account.InstanceID] {
			continue
		}
		if !ownedByGroup(account, group) {
			continue
		}
		missingKey := missingKeyForAccount(account.ID)
		missing, confirmErr := confirmCloudMissing(ctx, client, account.RegionID, account.InstanceID)
		if confirmErr != nil {
			failures = append(failures, fmt.Errorf("实例 %s 对账未完成：%w", account.InstanceID, confirmErr))
			continue
		}
		if !missing {
			_ = st.SetSetting(missingKey, "0")
			failures = append(failures, fmt.Errorf("实例 %s 未出现在列表中，但精确查询确认存在", account.InstanceID))
			continue
		}
		if confirmMissing {
			missingCount, _ := strconv.Atoi(st.GetSetting(missingKey, "0"))
			missingCount++
			_, _ = st.DB.Exec(`UPDATE accounts SET missing_count=?, missing_since=CASE WHEN missing_since=0 THEN ? ELSE missing_since END WHERE id=?`, missingCount, now.Unix(), account.ID)
			if err := st.SetSetting(missingKey, strconv.Itoa(missingCount)); err != nil {
				return result, err
			}
			if missingCount < 2 {
				st.AddLog("warning", "账号清单同步首次未发现实例，等待下次确认: "+account.InstanceID)
				continue
			}
		}
		_ = st.SetSetting(missingKey, "0")
		if account.InstanceStatus == "ReleaseFailed" && !account.EIPManaged {
			if removed, removeErr := st.PhysicallyDeleteReleaseFailed(account.ID); removeErr != nil {
				return result, removeErr
			} else if removed {
				st.AddLog("info", "已清理云端不存在的释放失败残留记录: "+account.InstanceID)
			}
			continue
		}
		if err := st.QueueMissingInstanceCleanup(account.ID, fmt.Sprintf("inventory-%d-%d", account.ID, now.UnixNano())); err != nil {
			return result, err
		}
		result.Purged++
		st.AddLog("info", "已同步移除云端不存在的实例: "+account.InstanceID)
	}
	return result, errors.Join(failures...)
}

// OrphanGroups returns one synthetic group per (access key, region) pair that
// still has local rows but is no longer described by the configured groups.
// Those rows are invisible to the normal per-group inventory pass, so they need
// a dedicated sweep.
func OrphanGroups(st *store.Store, groups []app.AccountGroup) ([]app.AccountGroup, error) {
	accounts, err := st.LoadAccounts(true)
	if err != nil {
		return nil, err
	}
	covered := make(map[string]bool, len(groups))
	for _, group := range groups {
		covered[group.AccessKeyID+"|"+group.RegionID] = true
	}
	orphans := make(map[string]app.AccountGroup)
	for _, account := range accounts {
		if account.AccessKeyID == "" || account.RegionID == "" {
			continue
		}
		key := account.AccessKeyID + "|" + account.RegionID
		if covered[key] {
			continue
		}
		if _, exists := orphans[key]; exists {
			continue
		}
		orphans[key] = app.AccountGroup{
			AccessKeyID: account.AccessKeyID, AccessKeySecret: account.AccessKeySecret,
			RegionID: account.RegionID, SiteType: account.SiteType,
		}
	}
	result := make([]app.AccountGroup, 0, len(orphans))
	for _, group := range orphans {
		result = append(result, group)
	}
	return result, nil
}

func missingKeyForAccount(accountID int64) string {
	return "inventory_missing_" + strconv.FormatInt(accountID, 10)
}

// PurgeAbsent removes local rows whose instance no longer exists in the cloud.
// It is the fallback for rows that no configured group covers, for example
// after a group was deleted or a region was dropped from settings, so the
// panel cannot keep showing instances that are gone from Aliyun.
func PurgeAbsent(ctx context.Context, st *store.Store, group app.AccountGroup, client cloud.Client, now time.Time, logf func(string, ...any)) (int, error) {
	accounts, err := st.LoadAccounts(true)
	if err != nil {
		return 0, err
	}
	purged := 0
	var failures []error
	for _, account := range accounts {
		// Only rows of the group's own cloud identity and region may be judged
		// with this client. Another account's row in the same region must never
		// be confirmed with the wrong access key.
		if account.InstanceID == "" || account.IsDeleted != 0 || !ownedByGroup(account, group) {
			continue
		}
		removed, purgeErr := purgeIfGone(ctx, st, account, group, client, now, logf)
		if purgeErr != nil {
			failures = append(failures, purgeErr)
			continue
		}
		if removed {
			purged++
		}
	}
	return purged, errors.Join(failures...)
}

func purgeIfGone(ctx context.Context, st *store.Store, account app.Account, group app.AccountGroup, client cloud.Client, now time.Time, logf func(string, ...any)) (bool, error) {
	// Confirm in the account's own region: a row whose region drifted must be
	// verified where the instance actually lives, not in the group's region,
	// otherwise a live instance could be purged by mistake.
	confirmRegion := account.RegionID
	if confirmRegion == "" {
		confirmRegion = group.RegionID
	}
	confirmedMissing, confirmErr := confirmCloudMissing(ctx, client, confirmRegion, account.InstanceID)
	if confirmErr != nil {
		if logf != nil {
			logf("二次确认云端实例失败（账号组 %s，实例 %s）: %v", group.GroupKey, account.InstanceID, confirmErr)
		}
		return false, fmt.Errorf("实例 %s 对账未完成：%w", account.InstanceID, confirmErr)
	}
	if !confirmedMissing {
		return false, nil
	}
	if err := st.QueueMissingInstanceCleanup(account.ID, fmt.Sprintf("inventory-orphan-%d-%d", account.ID, now.UnixNano())); err != nil {
		return false, err
	}
	return true, nil
}

func confirmCloudMissing(ctx context.Context, client cloud.Client, region, instanceID string) (bool, error) {
	instance, err := client.DescribeInstance(ctx, region, instanceID)
	if err != nil {
		if cloud.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	if instance == nil {
		return false, fmt.Errorf("DescribeInstance returned an empty response")
	}
	if instance.ID != instanceID {
		return false, fmt.Errorf("DescribeInstance returned %q while checking %q", instance.ID, instanceID)
	}
	// A successful response is authoritative even if the instance was absent
	// from the full list due to a provider-side list inconsistency.
	return false, nil
}

// ownedByGroup reports whether this group's cloud inventory is authoritative
// for a local row. A row whose stored group key drifted (for example after an
// account edit) still belongs to the same cloud identity, so the access key
// decides. Rows belonging to other accounts are never considered.
func ownedByGroup(account app.Account, group app.AccountGroup) bool {
	return account.AccessKeyID != "" && account.AccessKeyID == group.AccessKeyID && account.RegionID == group.RegionID
}
