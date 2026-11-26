package targetdb

import (
	"reflect"
	"testing"

	"dbmp/internal/model"
)

func keywords(stmts []Statement) []string {
	var out []string
	for _, s := range stmts {
		out = append(out, s.Keyword)
	}
	return out
}

func TestSplit(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{"simple", "CREATE TABLE a (id int); INSERT INTO a VALUES (1);", []string{"CREATE", "INSERT"}},
		{"no trailing semicolon", "select 1", []string{"SELECT"}},
		{"semicolon in string", "INSERT INTO t VALUES ('a;b'); SELECT 1", []string{"INSERT", "SELECT"}},
		{"doubled quote", "INSERT INTO t VALUES ('it''s;'); SELECT 2", []string{"INSERT", "SELECT"}},
		{"E string with backslash", `INSERT INTO t VALUES (E'a\';b'); SELECT 3`, []string{"INSERT", "SELECT"}},
		{"quoted identifier", `CREATE TABLE "we;ird" (x int); SELECT 4`, []string{"CREATE", "SELECT"}},
		{"line comment", "-- BEGIN; nothing\nSELECT 1; -- ; \nSELECT 2", []string{"SELECT", "SELECT"}},
		{"block comment", "/* a; /* nested; */ still; */ UPDATE t SET x = 1", []string{"UPDATE"}},
		{"dollar quote", "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql; SELECT f()", []string{"CREATE", "SELECT"}},
		{"tagged dollar quote", "DO $body$ BEGIN PERFORM 1; END $body$; SELECT 1", []string{"DO", "SELECT"}},
		{"empty statements", ";;  ; SELECT 1;;", []string{"SELECT"}},
		{"only comments", "-- nothing here\n/* none */", nil},
		{"parenthesised select", "(SELECT 1) UNION (SELECT 2)", []string{"SELECT"}},
	}
	for _, c := range cases {
		got := keywords(Split(c.sql))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSplitKeepsStatementText(t *testing.T) {
	stmts := Split("  ALTER TABLE a ADD COLUMN b text ;\nSELECT 'x;y'  ")
	if len(stmts) != 2 || stmts[0].Text != "ALTER TABLE a ADD COLUMN b text" || stmts[1].Text != "SELECT 'x;y'" {
		t.Fatalf("unexpected %+v", stmts)
	}
}

func TestTransactionControl(t *testing.T) {
	bad := []string{
		"BEGIN; CREATE TABLE a(); COMMIT;",
		"create table a(); commit",
		"START TRANSACTION; SELECT 1",
		"SELECT 1; ROLLBACK",
		"END",
		"ABORT",
		"PREPARE TRANSACTION 'x'",
	}
	for _, s := range bad {
		if len(TransactionControl(Split(s))) == 0 {
			t.Errorf("not detected: %q", s)
		}
	}
	good := []string{
		"CREATE FUNCTION f() RETURNS void AS $$ BEGIN NULL; END $$ LANGUAGE plpgsql",
		"DO $$ BEGIN RAISE NOTICE 'commit'; END $$",
		"INSERT INTO log(msg) VALUES ('BEGIN; COMMIT;')",
		"-- COMMIT\nSELECT 1",
		"SAVEPOINT a; RELEASE SAVEPOINT a",
		"PREPARE q AS SELECT 1",
	}
	for _, s := range good {
		if got := TransactionControl(Split(s)); len(got) != 0 {
			t.Errorf("false positive for %q: %v", s, got)
		}
	}
}

func TestExplainable(t *testing.T) {
	for kw, want := range map[string]bool{"SELECT": true, "UPDATE": true, "WITH": true, "CREATE": false, "ALTER": false} {
		if (Statement{Keyword: kw}).Explainable() != want {
			t.Errorf("%s explainable != %v", kw, want)
		}
	}
}

func TestValidate(t *testing.T) {
	normal := model.Migration{Name: "add column", Mode: model.ModeNormal,
		UpSQL: "ALTER TABLE a ADD COLUMN b int", DownSQL: "ALTER TABLE a DROP COLUMN b"}
	batch := model.Migration{Name: "bump", Mode: model.ModeBatch, BatchTable: "public.big", BatchColumn: "id", BatchSize: 500,
		UpSQL: "UPDATE big SET v = v + 1 WHERE id > $1 AND id <= $2", DownSQL: "UPDATE big SET v = v - 1 WHERE id > $1 AND id <= $2"}
	cases := []struct {
		name string
		base model.Migration
		edit func(*model.Migration)
		ok   bool
	}{
		{"valid", normal, func(*model.Migration) {}, true},
		{"no name", normal, func(m *model.Migration) { m.Name = "" }, false},
		{"empty up", normal, func(m *model.Migration) { m.UpSQL = "  -- nothing " }, false},
		{"manual tx", normal, func(m *model.Migration) { m.UpSQL = "BEGIN; SELECT 1; COMMIT;" }, false},
		{"manual tx in down", normal, func(m *model.Migration) { m.DownSQL = "ROLLBACK" }, false},
		{"no down", normal, func(m *model.Migration) { m.DownSQL = "" }, false},
		{"irreversible with reason", normal, func(m *model.Migration) { m.DownSQL, m.IrreversibleReason = "", "drops data" }, true},
		{"irreversible with down", normal, func(m *model.Migration) { m.IrreversibleReason = "drops data" }, false},
		{"bad mode", normal, func(m *model.Migration) { m.Mode = "fast" }, false},
		{"valid batch", batch, func(*model.Migration) {}, true},
		{"batch without params", batch, func(m *model.Migration) { m.UpSQL = "UPDATE big SET v = 1" }, false},
		{"batch two statements", batch, func(m *model.Migration) { m.UpSQL += "; SELECT $1, $2" }, false},
		{"batch bad table", batch, func(m *model.Migration) { m.BatchTable = "big; drop" }, false},
		{"batch size", batch, func(m *model.Migration) { m.BatchSize = 0 }, false},
	}
	for _, c := range cases {
		m := c.base
		c.edit(&m)
		if err := Validate(m); (err == nil) != c.ok {
			t.Errorf("%s: %v, want ok=%v", c.name, err, c.ok)
		}
	}
}
