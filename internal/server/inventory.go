package server

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JudiLite/ecs-controller/internal/app"
	"github.com/JudiLite/ecs-controller/internal/inventory"
)

func (s *Server) syncGroup(groupKey string) (int, error) {
	s.inventoryMu.Lock()
	defer s.inventoryMu.Unlock()
	return s.syncGroupContext(rctx(), groupKey, false)
}

// SyncInventory reconciles all configured account groups in the background.
// Automatic reconciliation requires two successful missing observations before
// it starts the existing local cleanup flow for an externally removed ECS.
func (s *Server) SyncInventory(ctx context.Context) (int, error) {
	s.inventoryMu.Lock()
	defer s.inventoryMu.Unlock()
	groups, err := s.Store.LoadGroups()
	if err != nil {
		return 0, err
	}
	type groupResult struct {
		key   string
		count int
		err   error
	}
	semaphore := make(chan struct{}, 3)
	results := make(chan groupResult, len(groups))
	var wg sync.WaitGroup
	for _, group := range groups {
		groupKey := group.GroupKey
		wg.Add(1)
		go func() {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			groupCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			count, syncErr := s.syncGroupContext(groupCtx, groupKey, true)
			results <- groupResult{key: groupKey, count: count, err: syncErr}
		}()
	}
	wg.Wait()
	close(results)

	total := 0
	var failures []string
	for result := range results {
		total += result.count
		if result.err != nil {
			failures = append(failures, result.key+": "+result.err.Error())
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return total, fmt.Errorf("部分账号组同步失败: %s", strings.Join(failures, "; "))
	}
	return total, nil
}

func (s *Server) syncGroupContext(ctx context.Context, groupKey string, confirmMissing bool) (int, error) {
	groups, err := s.Store.LoadGroups()
	if err != nil {
		return 0, err
	}
	var group *app.AccountGroup
	for i := range groups {
		if groups[i].GroupKey == groupKey {
			group = &groups[i]
			break
		}
	}
	if group == nil {
		return 0, fmt.Errorf("账号组不存在")
	}
	client := s.cloudClient(app.Account{AccessKeyID: group.AccessKeyID, AccessKeySecret: group.AccessKeySecret, RegionID: group.RegionID, SiteType: group.SiteType})
	if client == nil {
		return 0, fmt.Errorf("云客户端未配置")
	}
	result, err := inventory.SyncGroup(ctx, s.Store, *group, client, time.Now(), s.Log.Printf, confirmMissing)
	return result.RemoteCount, err
}

func inventoryMissingKey(accountID int64) string {
	return "inventory_missing_" + strconv.FormatInt(accountID, 10)
}
