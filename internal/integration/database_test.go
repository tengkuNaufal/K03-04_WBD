package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"dbmp/internal/authz"
)

type world struct {
	*env
	dbID                       int64
	viewer, editor, nina       *client
	outsider, admin            *client
	viewGrp, editGrp           int64
	viewerID, editorID, ninaID int64
}

func newWorld(t *testing.T) *world {
	e := newEnv(t)
	w := &world{env: e}
	w.viewerID = e.createUser("vera", false)
	w.editorID = e.createUser("eddie", false)
	w.ninaID = e.createUser("nina", false)
	e.createUser("otto", false)

	w.admin = e.client()
	w.admin.mustLogin("root")
	r := w.admin.post("/api/admin/databases", e.targetBody("shop"))
	expectStatus(t, "create connection", r, http.StatusCreated)
	w.dbID = r.id(t)

	r = w.admin.post("/api/admin/groups", map[string]string{"name": "viewers"})
	expectStatus(t, "create group", r, http.StatusCreated)
	w.viewGrp = r.id(t)
	w.editGrp = w.admin.post("/api/admin/groups", map[string]string{"name": "editors"}).id(t)

	for _, step := range []struct {
		method, path string
		body         any
	}{
		{"POST", fmt.Sprintf("/api/admin/groups/%d/members", w.viewGrp), map[string]int64{"user_id": w.viewerID}},
		{"POST", fmt.Sprintf("/api/admin/groups/%d/members", w.editGrp), map[string]int64{"user_id": w.editorID}},
		{"POST", fmt.Sprintf("/api/admin/groups/%d/members", w.editGrp), map[string]int64{"user_id": w.ninaID}},
		{"PUT", fmt.Sprintf("/api/admin/groups/%d/leader", w.viewGrp), map[string]int64{"user_id": w.viewerID}},
		{"PUT", fmt.Sprintf("/api/admin/groups/%d/leader", w.editGrp), map[string]int64{"user_id": w.editorID}},
		{"PUT", fmt.Sprintf("/api/admin/groups/%d/grants", w.viewGrp), map[string]any{"database_id": w.dbID, "level": "view"}},
		{"PUT", fmt.Sprintf("/api/admin/groups/%d/grants", w.editGrp), map[string]any{"database_id": w.dbID, "level": "edit"}},
	} {
		expectStatus(t, step.path, w.admin.send(step.method, step.path, step.body), http.StatusOK)
	}

	w.viewer, w.editor, w.nina, w.outsider = e.client(), e.client(), e.client(), e.client()
	w.viewer.mustLogin("vera")
	w.editor.mustLogin("eddie")
	w.nina.mustLogin("nina")
	w.outsider.mustLogin("otto")
	return w
}

func (w *world) api(format string, args ...any) string {
	return fmt.Sprintf("/api/db/%d", w.dbID) + fmt.Sprintf(format, args...)
}

func TestStoredPasswordIsEncrypted(t *testing.T) {
	w := newWorld(t)
	d, err := w.store.GetDatabase(context.Background(), w.dbID)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := w.targetURL.User.Password()
	if string(d.PasswordEnc) == pw {
		t.Fatal("target password stored in plaintext")
	}
	plain, err := w.box.Decrypt(d.PasswordEnc)
	if err != nil || string(plain) != pw {
		t.Fatalf("stored password does not decrypt to the original: %v", err)
	}
	r := w.admin.get(fmt.Sprintf("/api/admin/databases/%d", w.dbID))
	expectStatus(t, "connection", r, http.StatusOK)
	if strings.Contains(strings.ToLower(r.body), "password") {
		t.Fatalf("password returned by the API: %s", r.body)
	}
}

func TestGroupLeaderMustBeMember(t *testing.T) {
	w := newWorld(t)
	r := w.admin.send("PUT", fmt.Sprintf("/api/admin/groups/%d/leader", w.viewGrp), map[string]int64{"user_id": w.ninaID})
	expectStatus(t, "non-member leader", r, http.StatusBadRequest)
	expectStatus(t, "remove", w.admin.send("DELETE", fmt.Sprintf("/api/admin/groups/%d/members/%d", w.viewGrp, w.viewerID), nil), http.StatusOK)
	g, _ := w.store.GetGroup(context.Background(), w.viewGrp)
	if g.LeaderID != nil {
		t.Fatal("leader kept after leaving the group")
	}
}

func TestDatabaseViewer(t *testing.T) {
	w := newWorld(t)
	r := w.viewer.get("/api/databases")
	if !strings.Contains(r.body, `"name":"shop"`) || !strings.Contains(r.body, `"level":"view"`) {
		t.Fatalf("database list: %s", r.body)
	}

	r = w.viewer.get(w.api("/tables"))
	expectStatus(t, "tables", r, http.StatusOK)
	for _, s := range []string{"customers", "orders"} {
		if !strings.Contains(r.body, s) {
			t.Errorf("table %s not listed", s)
		}
	}
	r = w.viewer.get(w.api("/tables/public/orders"))
	expectStatus(t, "structure", r, http.StatusOK)
	for _, s := range []string{"customer_id", "numeric(10,2)", "FOREIGN KEY", "REFERENCES customers(id)", "CHECK", "orders_pkey"} {
		if !strings.Contains(r.body, s) {
			t.Errorf("structure misses %q", s)
		}
	}
	if r = w.viewer.get(w.api("/tables/public/customers")); !strings.Contains(r.body, "customers_city_idx") {
		t.Error("index not shown")
	}

	var page struct {
		Rows    [][]*string `json:"rows"`
		HasMore bool        `json:"has_more"`
	}
	w.viewer.get(w.api("/tables/public/customers/rows")).json(t, &page)
	if len(page.Rows) != 50 || !page.HasMore || *page.Rows[0][1] != "Customer 1" {
		t.Errorf("first page: %d rows, more=%v", len(page.Rows), page.HasMore)
	}
	w.viewer.get(w.api("/tables/public/customers/rows?offset=50")).json(t, &page)
	if len(page.Rows) != 25 || page.HasMore {
		t.Errorf("second page: %d rows, more=%v", len(page.Rows), page.HasMore)
	}

	expectStatus(t, "unknown table", w.viewer.get(w.api("/tables/public/nope")), http.StatusNotFound)
	expectStatus(t, "injection attempt", w.viewer.get(w.api("/tables/public/%s/rows", url.PathEscape(`customers"; DROP TABLE customers; --`))), http.StatusNotFound)
	var n int
	if err := w.target.QueryRow(context.Background(), "SELECT count(*) FROM customers").Scan(&n); err != nil || n != 75 {
		t.Fatal("customers table damaged")
	}
}

func TestUserWithoutGroupAccessIsDenied(t *testing.T) {
	w := newWorld(t)
	for _, p := range []string{w.api(""), w.api("/tables/public/customers/rows"), w.api("/migrations"), w.api("/history"), "/api/db/999999"} {
		expectStatus(t, p, w.outsider.get(p), http.StatusForbidden)
	}
	expectStatus(t, "admin without group", w.admin.get(w.api("")), http.StatusForbidden)
}

func TestAccessRemovalAppliesImmediately(t *testing.T) {
	w := newWorld(t)
	expectStatus(t, "before", w.viewer.get(w.api("")), http.StatusOK)
	r := w.admin.send("DELETE", fmt.Sprintf("/api/admin/groups/%d/members/%d", w.viewGrp, w.viewerID), nil)
	expectStatus(t, "remove member", r, http.StatusOK)
	expectStatus(t, "session revoked", w.viewer.get(w.api("")), http.StatusUnauthorized)
	w.viewer.mustLogin("vera")
	expectStatus(t, "after", w.viewer.get(w.api("")), http.StatusForbidden)

	r = w.admin.send("PUT", fmt.Sprintf("/api/admin/groups/%d/grants", w.editGrp), map[string]any{"database_id": w.dbID, "level": "view"})
	expectStatus(t, "downgrade", r, http.StatusOK)
	expectStatus(t, "editor revoked", w.editor.get(w.api("")), http.StatusUnauthorized)
}

func TestEffectiveAccessAcrossGroups(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if err := w.store.AddMember(ctx, w.editGrp, w.viewerID); err != nil {
		t.Fatal(err)
	}
	lvl, err := w.policy.Level(ctx, &authz.Principal{UserID: w.viewerID}, w.dbID)
	if err != nil || lvl != authz.LevelEdit {
		t.Fatalf("effective level = %v, %v", lvl, err)
	}
	dbs, _ := w.store.AccessibleDatabases(ctx, w.viewerID)
	if len(dbs) != 1 || dbs[0].Level != authz.LevelEdit {
		t.Fatalf("accessible databases: %+v", dbs)
	}
}

func TestUnreachableDatabase(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	broken := w.addDatabaseDirect("broken", "127.0.0.1", 1, "nope", "nobody", "x")
	if err := w.store.SetGrant(ctx, w.viewGrp, broken, authz.LevelView); err != nil {
		t.Fatal(err)
	}
	r := w.viewer.get(fmt.Sprintf("/api/db/%d/tables", broken))
	expectStatus(t, "unreachable", r, http.StatusBadGateway)
	if !strings.Contains(r.body, "Tidak dapat terhubung") || strings.Contains(r.body, "password") {
		t.Fatal("unclear or leaky error message")
	}

	body := w.targetBody("bad-creds")
	body["password"], body["port"] = "definitely-wrong", 1
	r = w.admin.post("/api/admin/databases", body)
	expectStatus(t, "bad connection", r, http.StatusBadRequest)
	if strings.Contains(r.body, "definitely-wrong") {
		t.Fatal("password echoed back")
	}
}
