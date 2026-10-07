package cloud

import (
	"context"
	"fmt"
	"time"
)

// CleanupEIP removes an EIP from every shared bandwidth package before
// releasing it. Alibaba Cloud keeps the package relation after an EIP is
// detached from an instance, so ReleaseEipAddress alone is not sufficient.
// The operation is idempotent and can be retried by a worker.
func CleanupEIP(ctx context.Context, client Client, region, allocationID string) error {
	if client == nil {
		return fmt.Errorf("cloud client is not configured")
	}
	if allocationID == "" {
		return nil
	}
	if shared, ok := client.(SharedBandwidthEIPClient); ok {
		if err := removeEIPFromSharedBandwidth(ctx, shared, region, allocationID); err != nil {
			return err
		}
	}
	return releaseEIPWithRetry(ctx, client, region, allocationID)
}

func removeEIPFromSharedBandwidth(ctx context.Context, client SharedBandwidthEIPClient, region, allocationID string) error {
	const attempts = 8
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(1500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				if lastErr != nil {
					return lastErr
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
		packages, err := client.DescribeCommonBandwidthPackages(ctx, region)
		if err != nil {
			lastErr = err
			if !IsEIPOperationPending(err) {
				return err
			}
			continue
		}
		packageIDs := commonBandwidthPackageIDsForEIP(packages, allocationID)
		if len(packageIDs) == 0 {
			return nil
		}
		lastErr = nil
		for _, packageID := range packageIDs {
			err := client.RemoveEIPFromCommonBandwidthPackage(ctx, region, packageID, allocationID)
			if err == nil || IsNotFound(err) || IsNotInCommonBandwidthPackage(err) {
				continue
			}
			lastErr = err
			if !IsEIPOperationPending(err) {
				return err
			}
		}
		if lastErr == nil {
			continue
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("shared bandwidth relation for EIP %s is still being removed", allocationID)
}

func commonBandwidthPackageIDsForEIP(packages []map[string]any, allocationID string) []string {
	ids := make([]string, 0)
	seen := map[string]bool{}
	for _, pkg := range packages {
		packageID := firstString(pkg, "id", "BandwidthPackageId", "bandwidthPackageId")
		if packageID == "" {
			continue
		}
		entries, ok := pkg["eips"].([]map[string]any)
		if !ok {
			if raw, exists := pkg["eips"].([]any); exists {
				entries = anyMaps(raw)
			}
		}
		for _, entry := range entries {
			if firstString(entry, "allocationId", "AllocationId", "IpInstanceId") == allocationID && !seen[packageID] {
				seen[packageID] = true
				ids = append(ids, packageID)
				break
			}
		}
	}
	return ids
}

func releaseEIPWithRetry(ctx context.Context, client Client, region, allocationID string) error {
	const attempts = 8
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(1500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				if lastErr != nil {
					return lastErr
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
		lastErr = client.ReleaseEIP(ctx, region, allocationID)
		if lastErr == nil || IsNotFound(lastErr) {
			return nil
		}
		if !IsEIPOperationPending(lastErr) {
			return lastErr
		}
	}
	return lastErr
}
