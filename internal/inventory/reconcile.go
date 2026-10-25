package inventory

import (
	"context"
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
			sameGroup := accounts[i].GroupKey == group.GroupKey || (accounts[i].AccessKeyID == group.AccessKeyID && accounts[i].RegionID == group.RegionID)
			if sameGroup && accounts[i].InstanceID == instance.ID {
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
	for _, account := range accounts {
		if account.InstanceID == "" || account.IsDeleted != 0 || remoteIDs[account.InstanceID] {
			continue
		}
		sameGroup := account.GroupKey == group.GroupKey || (account.AccessKeyID == group.AccessKeyID && account.RegionID == group.RegionID)
		if !sameGroup {
			continue
		}
		missingKey := missingKeyForAccount(account.ID)
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
		if account.InstanceStatus == "ReleaseFailed" {
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
	return result, nil
}

func missingKeyForAccount(accountID int64) string {
	return "inventory_missing_" + strconv.FormatInt(accountID, 10)
}
