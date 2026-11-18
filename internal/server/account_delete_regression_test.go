package server

import (
	"github.com/JudiLite/ecs-controller/internal/app"
	"github.com/JudiLite/ecs-controller/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeleteLastConfiguredAccountRemainsDeleted(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SaveGroups([]app.AccountGroup{{GroupKey: "g", AccessKeyID: "dummy", AccessKeySecret: "dummy", RegionID: "r"}})
	st.UpsertAccount(app.Account{GroupKey: "g", AccessKeyID: "dummy", AccessKeySecret: "dummy", RegionID: "r", InstanceID: "i", InstanceStatus: "Running"})
	srv := New(st, t.TempDir(), "")
	if err = srv.saveConfig(map[string]any{"Accounts": []any{}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		groups, e := st.LoadGroups()
		if e != nil || len(groups) != 0 {
			t.Fatal("deleted account reappeared")
		}
	}
	rows, err := st.LoadAccounts(true)
	if err != nil || len(rows) != 0 {
		t.Fatalf("instance remained: %+v %v", rows, err)
	}
}
func TestSetupCannotResetAnInitializedPassword(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := New(st, t.TempDir(), "")
	req := httptest.NewRequest(http.MethodPost, "/?action=setup", strings.NewReader(`{"admin_username":"admin","admin_password":"admin-password","traffic_threshold":95}`))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("initial setup rejected: %s", w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/?action=setup", strings.NewReader(`{"admin_password":"replacement"}`))
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 403 || !st.CheckAdminPassword("admin-password") {
		t.Fatal("setup reset an initialized password")
	}
}
