package server

import (
	"context"
	"github.com/JudiLite/ecs-controller/internal/app"
	"github.com/JudiLite/ecs-controller/internal/notify"
	"github.com/JudiLite/ecs-controller/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManualRotationDNSSyncUsesSavedCredentialsAndRunningTarget(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertAccount(app.Account{ID: 1, InstanceID: "i-test", InstanceStatus: "Running", PublicIP: "203.0.113.10"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveRotationGroups([]app.RotationGroup{{ID: "g", Domain: "svc.example.com", Provider: "cloudflare", DNS: app.DNSCredentials{CloudflareToken: "saved-token"}, Members: []app.RotationMember{{AccountID: 1}}}}); err != nil {
		t.Fatal(err)
	}
	srv := New(st, t.TempDir(), "")
	calls := 0
	srv.DNSUpdater = func(ctx context.Context, config notify.DNSUpdateConfig, address string) error {
		calls++
		if config.CloudflareToken != "saved-token" || address != "203.0.113.10" {
			t.Fatalf("wrong DNS request: %+v %s", config, address)
		}
		return nil
	}
	recorder := httptest.NewRecorder()
	srv.syncRotationDNS(recorder, httptest.NewRequest(http.MethodPost, "/", nil), map[string]any{"groupId": "g"})
	if recorder.Code != 200 || calls != 1 || strings.Contains(recorder.Body.String(), "saved-token") {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
	status := httptest.NewRecorder()
	srv.rotationDNSStatus(status)
	if !strings.Contains(status.Body.String(), "203.0.113.10") || strings.Contains(status.Body.String(), "saved-token") {
		t.Fatalf("invalid status: %s", status.Body.String())
	}
	denied := httptest.NewRecorder()
	srv.Handler().ServeHTTP(denied, httptest.NewRequest(http.MethodPost, "/index.php?action=sync_rotation_dns", strings.NewReader(`{"groupId":"g"}`)))
	if denied.Code != http.StatusForbidden || calls != 1 {
		t.Fatal("unauthenticated request changed DNS")
	}
}
