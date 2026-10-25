package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	webui "dbmp/web"
)

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                    "/",
		"/db/1":               "/db/1",
		"https://evil.test/x": "/",
		"//evil.test":         "/",
		`/\evil.test`:         "/",
		"javascript:alert(1)": "/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	r := httptest.NewRequest("POST", "http://app.local/x", nil)
	if !sameOrigin(r) {
		t.Error("no Origin header should pass (non-browser or same-origin GET form)")
	}
	r.Header.Set("Origin", "http://app.local")
	if !sameOrigin(r) {
		t.Error("same origin rejected")
	}
	r.Header.Set("Origin", "http://evil.test")
	if sameOrigin(r) {
		t.Error("cross origin accepted")
	}
	r.Header.Set("Origin", "null")
	if sameOrigin(r) {
		t.Error("opaque origin accepted")
	}
}

func TestValidCSRF(t *testing.T) {
	form := url.Values{csrfField: {"abc"}}
	r := httptest.NewRequest("POST", "/x", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if !validCSRF(r, "abc") {
		t.Error("matching token rejected")
	}
	r = httptest.NewRequest("POST", "/x", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if validCSRF(r, "other") {
		t.Error("wrong token accepted")
	}
	r = httptest.NewRequest("POST", "/x", nil)
	if validCSRF(r, "") || validCSRF(r, "abc") {
		t.Error("missing token accepted")
	}
}

func TestTemplatesParse(t *testing.T) {
	r, err := NewRenderer(webui.FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"login", "dashboard", "error", "db_tables", "db_table", "migrations",
		"migration", "admin_users", "admin_user", "admin_groups", "admin_group", "admin_databases", "admin_database"} {
		if _, ok := r.pages[name]; !ok {
			t.Errorf("missing page %s", name)
		}
	}
}
