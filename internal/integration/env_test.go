package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"dbmp/internal/auth"
	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/secret"
	"dbmp/internal/session"
	"dbmp/internal/store"
	"dbmp/internal/targetdb"
	"dbmp/internal/web"
	webui "dbmp/web"
)

func init() { auth.Cost = bcrypt.MinCost }

type env struct {
	t         *testing.T
	srv       *httptest.Server
	store     *store.Store
	box       *secret.Box
	targets   *targetdb.Manager
	policy    *authz.Policy
	sessions  *session.Manager
	target    *pgxpool.Pool 
	targetURL *url.URL
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func withDB(u *url.URL, name string) *url.URL {
	c := *u
	c.Path = "/" + name
	return &c
}

func createDB(t *testing.T, base *url.URL, name string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base.String())
	if err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), base.String())
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})
}

const seedTarget = `
CREATE TABLE customers (id serial PRIMARY KEY, name text NOT NULL, city text);
CREATE INDEX customers_city_idx ON customers(city);
INSERT INTO customers (name, city) SELECT 'Customer ' || g, 'Bandung' FROM generate_series(1, 75) g;
CREATE TABLE orders (id serial PRIMARY KEY, customer_id int NOT NULL REFERENCES customers(id), total numeric(10,2) CHECK (total >= 0));
`

func newEnv(t *testing.T) *env {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	base, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	suffix := randomSuffix()
	platformName, targetName := "dbmp_it_p_"+suffix, "dbmp_it_t_"+suffix
	createDB(t, base, platformName)
	createDB(t, base, targetName)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, withDB(base, platformName).String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	targetURL := withDB(base, targetName)
	target, err := pgxpool.New(ctx, targetURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(target.Close)
	if _, err := target.Exec(ctx, seedTarget); err != nil {
		t.Fatal(err)
	}

	st := store.New(pool)
	box, _ := secret.NewBox([]byte("integration-test-secret"))
	sessions := session.NewManager(st.Sessions(), session.DefaultConfig())
	policy := authz.NewPolicy(st)
	targets := targetdb.NewManager(st, box)
	t.Cleanup(targets.Close)
	h := &web.Server{Store: st, Sessions: sessions, Auth: auth.NewService(st, sessions), Policy: policy,
		Targets: targets, Box: box, Assets: webui.FS}
	if err := h.Init(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	e := &env{t: t, srv: srv, store: st, box: box, targets: targets, policy: policy, sessions: sessions,
		target: target, targetURL: targetURL}
	e.createUser("root", true)
	return e
}

func (e *env) createUser(name string, isAdmin bool) int64 {
	e.t.Helper()
	hash, _ := auth.HashPassword("password-" + name)
	id, err := e.store.CreateUser(context.Background(), name, hash, isAdmin)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) targetForm(name string) url.Values {
	pw, _ := e.targetURL.User.Password()
	host, port := e.targetURL.Hostname(), e.targetURL.Port()
	if port == "" {
		port = "5432"
	}
	return url.Values{
		"name": {name}, "host": {host}, "port": {port}, "dbname": {strings.TrimPrefix(e.targetURL.Path, "/")},
		"username": {e.targetURL.User.Username()}, "password": {pw}, "sslmode": {"disable"},
	}
}

func (e *env) addDatabaseDirect(name, host string, port int, dbname, user, pw string) int64 {
	e.t.Helper()
	enc, _ := e.box.Encrypt([]byte(pw))
	id, err := e.store.CreateDatabase(context.Background(), model.Database{
		Name: name, Host: host, Port: port, DBName: dbname, Username: user, PasswordEnc: enc, SSLMode: "disable",
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}


type client struct {
	e    *env
	http *http.Client
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: 30 * time.Second,
	}}
}

type resp struct {
	status   int
	body     string
	location string
	cookies  []*http.Cookie
}

func (c *client) do(req *http.Request) resp {
	c.e.t.Helper()
	res, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, body: string(b), location: res.Header.Get("Location"), cookies: res.Cookies()}
}

func (c *client) get(path string) resp {
	req, _ := http.NewRequest("GET", c.e.srv.URL+path, nil)
	return c.do(req)
}

func (c *client) postRaw(path string, form url.Values, headers map[string]string) resp {
	req, _ := http.NewRequest("POST", c.e.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.do(req)
}

var csrfRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func (c *client) csrf(path string) string {
	c.e.t.Helper()
	r := c.get(path)
	m := csrfRe.FindStringSubmatch(r.body)
	if m == nil {
		c.e.t.Fatalf("no csrf token on %s (status %d)", path, r.status)
	}
	return m[1]
}

func (c *client) post(path string, form url.Values) resp {
	c.e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf_token", c.csrf("/"))
	return c.postRaw(path, form, nil)
}

func (c *client) login(user, pw string) resp {
	c.e.t.Helper()
	token := c.csrf("/login")
	return c.postRaw("/login", url.Values{"username": {user}, "password": {pw}, "csrf_token": {token}}, nil)
}

func (c *client) mustLogin(user string) {
	c.e.t.Helper()
	r := c.login(user, "password-"+user)
	if r.status != http.StatusSeeOther {
		c.e.t.Fatalf("login %s: status %d", user, r.status)
	}
}

func (c *client) sessionCookie() string {
	u, _ := url.Parse(c.e.srv.URL)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == "dbmp_session" {
			return ck.Value
		}
	}
	return ""
}

var idRe = regexp.MustCompile(`/(\d+)$`)

func idFromLocation(t *testing.T, loc string) int64 {
	t.Helper()
	m := idRe.FindStringSubmatch(loc)
	if m == nil {
		t.Fatalf("no id in location %q", loc)
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	return id
}

func expectStatus(t *testing.T, what string, r resp, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("%s: status %d, want %d\n%s", what, r.status, want, firstLines(r.body, 40))
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
