package ddns

import (
	"context"
	"errors"
	"github.com/JudiLite/ecs-controller/internal/app"
	"github.com/JudiLite/ecs-controller/internal/notify"
	"github.com/JudiLite/ecs-controller/internal/store"
	"testing"
	"time"
)

func TestSyncRefreshesChangedIPAndConfigWithoutDailyDelay(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	group := app.RotationGroup{ID: "g", Domain: "svc.example.com", Provider: "cloudflare", DNS: app.DNSCredentials{CloudflareToken: "token"}, Members: []app.RotationMember{{AccountID: 1}}}
	accounts := []app.Account{{ID: 1, InstanceStatus: "Running", PublicIP: "203.0.113.1"}}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	calls := 0
	updater := func(_ context.Context, config notify.DNSUpdateConfig, address string) error {
		calls++
		if config.Domain != group.Domain || address != accounts[0].PublicIP {
			t.Fatalf("wrong DNS target: %+v %s", config, address)
		}
		return nil
	}
	for _, offset := range []time.Duration{0, time.Minute} {
		if _, err := Sync(context.Background(), st, group, accounts, now.Add(offset), false, updater); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("unchanged IP issued %d writes", calls)
	}
	accounts[0].PublicIP = "203.0.113.2"
	if _, err := Sync(context.Background(), st, group, accounts, now.Add(2*time.Minute), false, updater); err != nil {
		t.Fatal(err)
	}
	group.DNS.TTL = 300
	if _, err := Sync(context.Background(), st, group, accounts, now.Add(3*time.Minute), false, updater); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(context.Background(), st, group, accounts, now.Add(4*time.Minute), true, updater); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || Latest(st, "g").Address != "203.0.113.2" {
		t.Fatalf("IP/config/manual sync calls=%d result=%+v", calls, Latest(st, "g"))
	}
}

func TestSyncRetriesFailureAndDoesNotPublishStoppedInstances(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	group := app.RotationGroup{ID: "g", Domain: "svc.example.com", Provider: "alidns", Members: []app.RotationMember{{AccountID: 1}}}
	accounts := []app.Account{{ID: 1, InstanceStatus: "Running", PublicIP: "203.0.113.1"}}
	now := time.Now()
	calls := 0
	updater := func(context.Context, notify.DNSUpdateConfig, string) error {
		calls++
		if calls == 1 {
			return errors.New("permission denied")
		}
		return nil
	}
	if _, err := Sync(context.Background(), st, group, accounts, now, false, updater); err == nil {
		t.Fatal("provider error hidden")
	}
	if _, err := Sync(context.Background(), st, group, accounts, now.Add(30*time.Second), false, updater); err == nil || calls != 1 {
		t.Fatal("retry backoff missing")
	}
	if _, err := Sync(context.Background(), st, group, accounts, now.Add(61*time.Second), false, updater); err != nil || calls != 2 {
		t.Fatalf("retry failed: %v %d", err, calls)
	}
	accounts[0].InstanceStatus = "Stopped"
	if _, err := Sync(context.Background(), st, group, accounts, now.Add(2*time.Minute), true, updater); err == nil || calls != 2 {
		t.Fatal("stopped target was published")
	}
}

func TestTargetChoosesIncomingInstanceDuringRotationOverlap(t *testing.T) {
	group := app.RotationGroup{AdvanceStartMinutes: 10, Members: []app.RotationMember{{AccountID: 1}, {AccountID: 2}}}
	accounts := []app.Account{
		{ID: 1, InstanceStatus: "Running", PublicIP: "203.0.113.1", ScheduleEnabled: true, ScheduleStartEnabled: true, StartTime: "00:00"},
		{ID: 2, InstanceStatus: "Running", PublicIPMode: "eip", EIPAddress: "203.0.113.2", ScheduleEnabled: true, ScheduleStartEnabled: true, StartTime: "12:00"},
	}
	target, ok := Target(group, accounts, time.Date(2026, 10, 26, 11, 55, 0, 0, time.UTC))
	if !ok || target.ID != 2 || Address(target) != "203.0.113.2" {
		t.Fatalf("wrong handover target: %+v", target)
	}
}
