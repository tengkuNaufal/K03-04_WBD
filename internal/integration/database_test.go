package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"dbmp/internal/authz"
	"dbmp/internal/model"
)

type world struct {
	*env
	dbID               int64
	viewer, editor     *client
	outsider, admin    *client
	viewGrp, editGrp   int64
	viewerID, editorID int64
}

func newWorld(t *testing.T) *world {
	e := newEnv(t)
	w := &world{env: e}
	w.viewerID = e.createUser("vera", false)
	w.editorID = e.createUser("eddie", false)
	e.createUser("otto", false)

	w.admin = e.client()
	w.admin.mustLogin("root")

	r := w.admin.post("/admin/databases", e.targetForm("shop"))
	expectStatus(t, "create connection", r, http.StatusSeeOther)
	w.dbID = idFromLocation(t, r.location)

	r = w.admin.post("/admin/groups", url.Values{"name": {"viewers"}})
	expectStatus(t, "create group", r, http.StatusSeeOther)
	w.viewGrp = idFromLocation(t, r.location)
	r = w.admin.post("/admin/groups", url.Values{"name": {"editors"}})
	w.editGrp = idFromLocation(t, r.location)

	for _, step := range []struct {
		path string
		form url.Values
	}{
		{fmt.Sprintf("/admin/groups/%d/members", w.viewGrp), url.Values{"user_id": {fmt.Sprint(w.viewerID)}}},
		{fmt.Sprintf("/admin/groups/%d/members", w.editGrp), url.Values{"user_id": {fmt.Sprint(w.editorID)}}},
		{fmt.Sprintf("/admin/groups/%d/grants", w.viewGrp), url.Values{"database_id": {fmt.Sprint(w.dbID)}, "level": {"view"}}},
		{fmt.Sprintf("/admin/groups/%d/grants", w.editGrp), url.Values{"database_id": {fmt.Sprint(w.dbID)}, "level": {"edit"}}},
	} {
		expectStatus(t, step.path, w.admin.post(step.path, step.form), http.StatusSeeOther)
	}

	w.viewer, w.editor, w.outsider = e.client(), e.client(), e.client()
	w.viewer.mustLogin("vera")
	w.editor.mustLogin("eddie")
	w.outsider.mustLogin("otto")
	return w
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
	// The edit form never pre-fills the password field.
	page := w.admin.get(fmt.Sprintf("/admin/databases/%d", w.dbID))
	if !strings.Contains(page.body, `<input type="password" name="password" autocomplete="new-password" placeholder=`) {
		t.Fatal("password field must be empty on the edit form")
	}
}

func TestDatabaseViewer(t *testing.T) {
	w := newWorld(t)

	home := w.viewer.get("/")
	if !strings.Contains(home.body, "shop") || !strings.Contains(home.body, "View Only") {
		t.Fatal("dashboard should list the database with its level")
	}

	r := w.viewer.get(fmt.Sprintf("/db/%d", w.dbID))
	expectStatus(t, "tables", r, http.StatusOK)
	for _, s := range []string{"customers", "orders"} {
		if !strings.Contains(r.body, s) {
			t.Errorf("table %s not listed", s)
		}
	}

	r = w.viewer.get(fmt.Sprintf("/db/%d/tables/public/orders", w.dbID))
	expectStatus(t, "structure", r, http.StatusOK)
	for _, s := range []string{"customer_id", "numeric(10,2)", "FOREIGN KEY", "REFERENCES customers(id)", "CHECK", "orders_pkey"} {
		if !strings.Contains(r.body, s) {
			t.Errorf("structure page misses %q", s)
		}
	}
	r = w.viewer.get(fmt.Sprintf("/db/%d/tables/public/customers", w.dbID))
	if !strings.Contains(r.body, "customers_city_idx") {
		t.Error("index not shown")
	}

	r = w.viewer.get(fmt.Sprintf("/db/%d/tables/public/customers?tab=data&limit=50", w.dbID))
	expectStatus(t, "data", r, http.StatusOK)
	if !strings.Contains(r.body, "Customer 1<") || !strings.Contains(r.body, "Berikutnya") {
		t.Error("first data page wrong")
	}
	r = w.viewer.get(fmt.Sprintf("/db/%d/tables/public/customers?tab=data&limit=50&offset=50", w.dbID))
	if !strings.Contains(r.body, "Customer 75<") || strings.Contains(r.body, "Berikutnya") {
		t.Error("second data page wrong")
	}

	expectStatus(t, "unknown table", w.viewer.get(fmt.Sprintf("/db/%d/tables/public/nope", w.dbID)), http.StatusNotFound)
	// Identifiers are quoted, so this is just an unknown table, not SQL.
	expectStatus(t, "injection attempt", w.viewer.get(fmt.Sprintf("/db/%d/tables/public/%s?tab=data", w.dbID, url.PathEscape(`customers"; DROP TABLE customers; --`))), http.StatusNotFound)
	var n int
	if err := w.target.QueryRow(context.Background(), "SELECT count(*) FROM customers").Scan(&n); err != nil || n != 75 {
		t.Fatal("customers table damaged")
	}
}

func TestUserWithoutGroupAccessIsDenied(t *testing.T) {
	w := newWorld(t)
	for _, p := range []string{
		fmt.Sprintf("/db/%d", w.dbID),
		fmt.Sprintf("/db/%d/tables/public/customers?tab=data", w.dbID),
		fmt.Sprintf("/db/%d/migrations", w.dbID),
		"/db/999999",
	} {
		expectStatus(t, p, w.outsider.get(p), http.StatusForbidden)
	}
	// Platform Admin is not a database access level.
	expectStatus(t, "admin without group", w.admin.get(fmt.Sprintf("/db/%d", w.dbID)), http.StatusForbidden)
}

func TestAccessRemovalAppliesImmediately(t *testing.T) {
	w := newWorld(t)
	path := fmt.Sprintf("/db/%d", w.dbID)
	expectStatus(t, "before", w.viewer.get(path), http.StatusOK)
	r := w.admin.post(fmt.Sprintf("/admin/groups/%d/members/%d/remove", w.viewGrp, w.viewerID), nil)
	expectStatus(t, "remove member", r, http.StatusSeeOther)
	// The session is ended in the backend; after logging in again the
	// database is still out of reach.
	r = w.viewer.get(path)
	if r.status != http.StatusSeeOther || !strings.HasPrefix(r.location, "/login") {
		t.Fatalf("session not revoked: %d", r.status)
	}
	w.viewer.mustLogin("vera")
	expectStatus(t, "after", w.viewer.get(path), http.StatusForbidden)

	// Lowering a group's level also ends the members' sessions.
	r = w.admin.post(fmt.Sprintf("/admin/groups/%d/grants", w.editGrp), url.Values{"database_id": {fmt.Sprint(w.dbID)}, "level": {"view"}})
	expectStatus(t, "downgrade", r, http.StatusSeeOther)
	if r := w.editor.get(path); r.status != http.StatusSeeOther {
		t.Fatalf("editor session not revoked after downgrade: %d", r.status)
	}
}

func TestEffectiveAccessAcrossGroups(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	// vera joins the editors group too: View Only + View + Edit = View + Edit.
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

func TestMigrationLifecycle(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	base := fmt.Sprintf("/db/%d/migrations", w.dbID)

	// View Only users cannot create migrations, even with a direct request.
	expectStatus(t, "viewer new form", w.viewer.get(base+"/new"), http.StatusForbidden)
	expectStatus(t, "viewer create", w.viewer.post(base, url.Values{"name": {"x"}, "sql": {"SELECT 1"}}), http.StatusForbidden)

	// Editor creates a draft.
	r := w.editor.post(base, url.Values{"name": {"add email"}, "sql": {"ALTER TABLE customers ADD COLUMN email text;\nCREATE INDEX customers_email_idx ON customers(email);"}})
	expectStatus(t, "create", r, http.StatusSeeOther)
	good := idFromLocation(t, r.location)

	// Manual transaction control is rejected.
	r = w.editor.post(base, url.Values{"name": {"tx"}, "sql": {"BEGIN; SELECT 1; COMMIT;"}})
	expectStatus(t, "manual tx", r, http.StatusBadRequest)

	// Viewer can see but not run it.
	expectStatus(t, "viewer detail", w.viewer.get(fmt.Sprintf("%s/%d", base, good)), http.StatusOK)
	expectStatus(t, "viewer execute", w.viewer.post(fmt.Sprintf("%s/%d/execute", base, good), nil), http.StatusForbidden)
	expectStatus(t, "viewer edit", w.viewer.post(fmt.Sprintf("%s/%d", base, good), url.Values{"name": {"x"}, "sql": {"DROP TABLE customers"}}), http.StatusForbidden)

	// Execute: committed as version 1.
	expectStatus(t, "execute", w.editor.post(fmt.Sprintf("%s/%d/execute", base, good), nil), http.StatusSeeOther)
	var exists bool
	_ = w.target.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='customers' AND column_name='email')`).Scan(&exists)
	if !exists {
		t.Fatal("migration not applied to target database")
	}
	m, _ := w.store.GetMigration(ctx, w.dbID, good)
	if m.Status != model.MigrationApplied || m.Version == nil || *m.Version != 1 || m.ExecutedBy != "eddie" || m.ExecutedAt == nil {
		t.Fatalf("migration record: %+v", m)
	}

	// Applied migrations are immutable.
	r = w.editor.post(fmt.Sprintf("%s/%d", base, good), url.Values{"name": {"changed"}, "sql": {"SELECT 1"}})
	expectStatus(t, "edit applied", r, http.StatusBadRequest)
	w.editor.post(fmt.Sprintf("%s/%d/execute", base, good), nil)
	if m, _ = w.store.GetMigration(ctx, w.dbID, good); *m.Version != 1 {
		t.Fatal("applied migration ran twice")
	}

	// A failing migration is rolled back completely.
	r = w.editor.post(base, url.Values{"name": {"broken"}, "sql": {"CREATE TABLE half_done (id int);\nINSERT INTO customers (name) VALUES ('x');\nINSERT INTO missing_table VALUES (1);"}})
	bad := idFromLocation(t, r.location)
	expectStatus(t, "execute broken", w.editor.post(fmt.Sprintf("%s/%d/execute", base, bad), nil), http.StatusSeeOther)
	_ = w.target.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_tables WHERE tablename='half_done')`).Scan(&exists)
	var customers int
	_ = w.target.QueryRow(ctx, `SELECT count(*) FROM customers`).Scan(&customers)
	if exists || customers != 75 {
		t.Fatalf("partial changes kept: table=%v customers=%d", exists, customers)
	}
	m, _ = w.store.GetMigration(ctx, w.dbID, bad)
	if m.Status != model.MigrationFailed || m.Version != nil || !strings.Contains(m.Error, "missing_table") {
		t.Fatalf("failed migration record: %+v", m)
	}

	// Fix it and run again: becomes version 2.
	expectStatus(t, "fix", w.editor.post(fmt.Sprintf("%s/%d", base, bad), url.Values{"name": {"fixed"}, "sql": {"CREATE TABLE half_done (id int);"}}), http.StatusSeeOther)
	expectStatus(t, "execute fixed", w.editor.post(fmt.Sprintf("%s/%d/execute", base, bad), nil), http.StatusSeeOther)
	m, _ = w.store.GetMigration(ctx, w.dbID, bad)
	if m.Status != model.MigrationApplied || *m.Version != 2 {
		t.Fatalf("fixed migration: %+v", m)
	}

	// History page shows the linear history.
	r = w.viewer.get(base)
	expectStatus(t, "history", r, http.StatusOK)
	for _, s := range []string{"v1", "v2", "add email", "fixed", "eddie"} {
		if !strings.Contains(r.body, s) {
			t.Errorf("history page misses %q", s)
		}
	}

	// Applied migrations cannot be deleted; drafts can.
	expectStatus(t, "delete applied", w.editor.post(fmt.Sprintf("%s/%d/delete", base, good), nil), http.StatusSeeOther)
	if _, err := w.store.GetMigration(ctx, w.dbID, good); err != nil {
		t.Fatal("applied migration deleted")
	}
	r = w.editor.post(base, url.Values{"name": {"tmp"}, "sql": {"SELECT 1"}})
	tmp := idFromLocation(t, r.location)
	expectStatus(t, "delete draft", w.editor.post(fmt.Sprintf("%s/%d/delete", base, tmp), nil), http.StatusSeeOther)
	if _, err := w.store.GetMigration(ctx, w.dbID, tmp); err == nil {
		t.Fatal("draft not deleted")
	}
}

func TestExplainDoesNotChangeData(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	base := fmt.Sprintf("/db/%d/migrations", w.dbID)
	r := w.editor.post(base, url.Values{"name": {"bulk update"}, "sql": {"ALTER TABLE customers ADD COLUMN vip boolean;\nUPDATE customers SET city = 'Jakarta' WHERE id < 10;\nSELECT c.name FROM customers c JOIN orders o ON o.customer_id = c.id;"}})
	id := idFromLocation(t, r.location)

	r = w.editor.post(fmt.Sprintf("%s/%d/explain", base, id), url.Values{"analyze": {"1"}})
	expectStatus(t, "explain", r, http.StatusOK)
	for _, s := range []string{"actual time", "Update on public.customers", "Join", "tidak dapat di-EXPLAIN"} {
		if !strings.Contains(r.body, s) {
			t.Errorf("explain output misses %q", s)
		}
	}
	var jakarta int
	_ = w.target.QueryRow(ctx, `SELECT count(*) FROM customers WHERE city = 'Jakarta'`).Scan(&jakarta)
	var vip bool
	_ = w.target.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='customers' AND column_name='vip')`).Scan(&vip)
	if jakarta != 0 || vip {
		t.Fatal("EXPLAIN ANALYZE changes were not rolled back")
	}
	expectStatus(t, "viewer explain", w.viewer.post(fmt.Sprintf("%s/%d/explain", base, id), nil), http.StatusForbidden)
}

func TestConcurrentExecutionKeepsVersionsLinear(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := w.store.CreateMigration(ctx, w.dbID, fmt.Sprintf("t%d", i), fmt.Sprintf("CREATE TABLE t%d (id int)", i), "eddie")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, id := range ids {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_, err := w.store.RunMigration(ctx, w.dbID, id, "eddie", func(sql string) (string, error) {
				return w.targets.Exec(ctx, w.dbID, sql)
			})
			if err != nil {
				errs <- err
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	ms, _ := w.store.ListMigrations(ctx, w.dbID)
	for i, m := range ms {
		if m.Version == nil || *m.Version != i+1 {
			t.Fatalf("versions not linear: %d -> %v", i, m.Version)
		}
	}
}

func TestUnreachableDatabase(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	broken := w.addDatabaseDirect("broken", "127.0.0.1", 1, "nope", "nobody", "x")
	if err := w.store.SetGrant(ctx, w.viewGrp, broken, authz.LevelView); err != nil {
		t.Fatal(err)
	}
	r := w.viewer.get(fmt.Sprintf("/db/%d", broken))
	expectStatus(t, "unreachable", r, http.StatusBadGateway)
	if !strings.Contains(r.body, "Tidak dapat terhubung") || strings.Contains(r.body, "password") {
		t.Fatal("unclear or leaky error message")
	}

	// Admin form rejects a connection that does not work.
	form := w.targetForm("bad-creds")
	form.Set("password", "definitely-wrong")
	form.Set("port", "1")
	r = w.admin.post("/admin/databases", form)
	expectStatus(t, "bad connection", r, http.StatusBadRequest)
	if strings.Contains(r.body, "definitely-wrong") {
		t.Fatal("password echoed back")
	}
}
