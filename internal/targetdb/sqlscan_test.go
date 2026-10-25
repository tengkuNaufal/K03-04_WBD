package targetdb

import (
	"reflect"
	"testing"
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
	cases := []struct {
		name, sql string
		ok        bool
	}{
		{"add column", "ALTER TABLE a ADD COLUMN b int", true},
		{"", "SELECT 1", false},
		{"empty sql", "  -- nothing ", false},
		{"manual tx", "BEGIN; SELECT 1; COMMIT;", false},
	}
	for _, c := range cases {
		err := Validate(c.name, c.sql)
		if (err == nil) != c.ok {
			t.Errorf("Validate(%q, %q) = %v, want ok=%v", c.name, c.sql, err, c.ok)
		}
	}
}
