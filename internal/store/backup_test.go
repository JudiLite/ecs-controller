package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JudiLite/ecs-controller/internal/app"
)

func TestBackupEncryptedPortableRoundTrip(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	source, err := Open(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := source.SetAdminCredentials("source_admin", "source-password"); err != nil {
		t.Fatal(err)
	}
	group := app.AccountGroup{GroupKey: "group-hk", Remark: "Primary", AccessKeyID: "test-ak", AccessKeySecret: "cloud-secret-value", RegionID: "cn-hongkong", MaxTraffic: 300}
	if err := source.SaveGroups([]app.AccountGroup{group}); err != nil {
		t.Fatal(err)
	}
	if err := source.UpsertAccount(app.Account{ID: 42, GroupKey: group.GroupKey, AccessKeyID: group.AccessKeyID, AccessKeySecret: group.AccessKeySecret, RegionID: group.RegionID, InstanceID: "i-portable", MaxTraffic: 300}); err != nil {
		t.Fatal(err)
	}
	if err := source.SaveRotationGroups([]app.RotationGroup{{ID: "rotation", Name: "rotation", DNS: app.DNSCredentials{CloudflareToken: "dns-secret-value"}, Members: []app.RotationMember{{AccountID: 42}}}}); err != nil {
		t.Fatal(err)
	}
	notification, _ := source.Seal("notification-secret")
	_ = source.SetSetting("notify_tg_token", notification)
	_ = source.SetSetting("custom_title", "ENC1 is just a title")
	_ = source.SetSetting("traffic_threshold", "90")
	// PNG signature is enough for content sniffing; image decoding belongs to upload.
	if err := os.WriteFile(filepath.Join(sourceDir, "brand-logo.png"), append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...), 0600); err != nil {
		t.Fatal(err)
	}
	_ = source.SetSetting("app_logo_url", "index.php?action=brand_logo")
	source.AddLog("info", "historical message")
	password := "portable-backup-password"
	raw, err := source.CreateBackup(ctx, password)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"cloud-secret-value", "notification-secret", "historical message", "test-ak"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("backup leaks %q", secret)
		}
	}
	if _, err := DecodeBackup(raw, "wrong-password-here"); err == nil {
		t.Fatal("wrong password accepted")
	}
	tampered := bytes.Clone(raw)
	tampered[len(tampered)-1] ^= 1
	if _, err := DecodeBackup(tampered, password); err == nil {
		t.Fatal("tampering accepted")
	}
	backup, err := DecodeBackup(raw, password)
	if err != nil {
		t.Fatal(err)
	}
	if got := backup.Summary(); got.Accounts != 1 || got.Instances != 1 || got.Logs != 1 {
		t.Fatalf("summary=%+v", got)
	}
	target, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := target.SetAdminCredentials("target_admin", "target-password"); err != nil {
		t.Fatal(err)
	}
	_ = target.CreateSession("session", "csrf", time.Hour)
	key := target.Key
	if err := target.RestoreBackup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if key != target.Key || !target.CheckAdminCredentials("target_admin", "target-password") {
		t.Fatal("local authentication/key overwritten")
	}
	if _, ok := target.Session("session"); ok {
		t.Fatal("old session remains valid")
	}
	groups, err := target.LoadGroups()
	if err != nil || len(groups) != 1 || groups[0].AccessKeySecret != group.AccessKeySecret {
		t.Fatalf("group secret round trip failed: %+v %v", groups, err)
	}
	account, err := target.Account(42, false)
	if err != nil || account.AccessKeySecret != group.AccessKeySecret {
		t.Fatalf("account round trip failed: %v", err)
	}
	rotations, err := target.LoadRotationGroups()
	if err != nil || len(rotations) != 1 || rotations[0].DNS.CloudflareToken != "dns-secret-value" {
		t.Fatalf("rotation secret round trip failed: %+v %v", rotations, err)
	}
	if secret, err := target.OpenSecret(target.GetSetting("notify_tg_token", "")); err != nil || secret != "notification-secret" {
		t.Fatalf("notification round trip: %q %v", secret, err)
	}
	if !strings.HasPrefix(target.GetSetting("app_logo_url", ""), "data:image/png;base64,") {
		t.Fatal("uploaded logo not portable")
	}
	if target.GetSetting("traffic_threshold", "") != "90" {
		t.Fatal("config not restored")
	}
	if target.GetSetting("custom_title", "") != "ENC1 is just a title" {
		t.Fatal("ordinary setting mistaken for encrypted secret")
	}
}

func TestBackupRestoreRollbackAndQueueProtection(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.SetSetting("traffic_threshold", "80")
	raw, err := st.CreateBackup(context.Background(), "valid-backup-password")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DecodeBackup(raw, "valid-backup-password")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetSetting("traffic_threshold", "99")
	// Fail a later table after settings have been replaced inside the transaction.
	bad := b.Tables["accounts"]
	bad.Columns[0] = "unknown_column"
	b.Tables["accounts"] = bad
	if err := st.RestoreBackup(context.Background(), b); err == nil {
		t.Fatal("incompatible schema accepted")
	}
	if st.GetSetting("traffic_threshold", "") != "99" {
		t.Fatal("partial restore committed")
	}
	b, err = DecodeBackup(raw, "valid-backup-password")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.Exec(`INSERT INTO jobs(job_id,kind,entity_key,status,available_at,created_at,updated_at) VALUES('pending','delete_instance','1','queued',0,0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RestoreBackup(context.Background(), b); err == nil {
		t.Fatal("restore accepted with active cloud task")
	}
	if st.GetSetting("traffic_threshold", "") != "99" {
		t.Fatal("queue rejection modified data")
	}
}

func TestBackupDecoderRejectsMalformedInput(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("not a backup"), append([]byte(backupMagic), make([]byte, 25)...)} {
		if _, err := DecodeBackup(raw, "valid-backup-password"); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateBackup(context.Background(), "短短短短短"); err == nil {
		t.Fatal("short unicode password accepted")
	}
	// JSON numeric data retains its original precision in exported history.
	value := json.Number("9007199254740993")
	got, err := st.backupValue(value, st)
	if err != nil || got != "9007199254740993" {
		t.Fatalf("numeric precision: %v %v", got, err)
	}
}
