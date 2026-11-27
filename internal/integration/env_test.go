// Package integration tests real components together: the HTTP API handlers,
// the worker, the platform store and real PostgreSQL databases (a fresh
// platform and target database per test). Set TEST_DATABASE_URL to run them.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
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
	"dbmp/internal/worker"
)

func init() {
	auth.Cost = bcrypt.MinCost
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type env struct {
	t         *testing.T
	srv       *httptest.Server
	store     *store.Store
	box       *secret.Box
	targets   *targetdb.Manager
	policy    *authz.Policy
	sessions  *session.Manager
	target    *pgxpool.Pool // direct connection to the target database
	targetURL *url.URL
	base      *url.URL
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
INSERT INTO orders (customer_id, total) SELECT 1 + g % 75, g FROM generate_series(1, 1000) g;
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
		Targets: targets, Box: box, Assets: fstest.MapFS{"index.html": {Data: []byte("<div id=root></div>")}}}
	if err := h.Init(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	e := &env{t: t, srv: srv, store: st, box: box, targets: targets, policy: policy, sessions: sessions,
		target: target, targetURL: targetURL, base: base}
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

// worker returns a worker bound to this environment.
func (e *env) worker(id string) *worker.Worker {
	return &worker.Worker{Store: e.store, Targets: e.targets, ID: id, Heartbeat: 50 * time.Millisecond, StaleAfter: time.Second}
}

// drain runs queued jobs until the queue has nothing runnable.
func (e *env) drain() {
	e.t.Helper()
	w := e.worker("test-worker")
	for i := 0; i < 50; i++ {
		found, err := w.RunOnce(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		if !found {
			return
		}
	}
	e.t.Fatal("queue did not drain")
}

func (e *env) targetBody(name string) map[string]any {
	pw, _ := e.targetURL.User.Password()
	port, _ := strconv.Atoi(e.targetURL.Port())
	if port == 0 {
		port = 5432
	}
	return map[string]any{
		"name": name, "host": e.targetURL.Hostname(), "port": port, "dbname": strings.TrimPrefix(e.targetURL.Path, "/"),
		"username": e.targetURL.User.Username(), "password": pw, "sslmode": "disable",
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

// ---- HTTP client ------------------------------------------------------------------

type client struct {
	e    *env
	http *http.Client
	csrf string
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
}

type resp struct {
	status  int
	body    string
	cookies []*http.Cookie
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
}

// id returns the "id" (or "job_id") field of a JSON body.
func (r resp) id(t *testing.T) int64 {
	t.Helper()
	var v struct {
		ID    int64 `json:"id"`
		JobID int64 `json:"job_id"`
	}
	r.json(t, &v)
	return max(v.ID, v.JobID)
}

func (c *client) do(method, path string, body any, headers map[string]string) resp {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, body: string(b), cookies: res.Cookies()}
}

func (c *client) get(path string) resp { return c.do("GET", path, nil, nil) }

// send makes a state-changing request with the session's CSRF token.
func (c *client) send(method, path string, body any) resp {
	if body == nil {
		body = map[string]any{}
	}
	return c.do(method, path, body, map[string]string{"X-CSRF-Token": c.csrf})
}

func (c *client) post(path string, body any) resp { return c.send("POST", path, body) }

func (c *client) login(user, pw string) resp {
	r := c.do("POST", "/api/login", map[string]string{"username": user, "password": pw}, nil)
	if r.status == http.StatusOK {
		var v struct{ CSRF string }
		_ = json.Unmarshal([]byte(r.body), &v)
		c.csrf = v.CSRF
	}
	return r
}

func (c *client) mustLogin(user string) {
	c.e.t.Helper()
	if r := c.login(user, "password-"+user); r.status != http.StatusOK {
		c.e.t.Fatalf("login %s: status %d %s", user, r.status, r.body)
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

func expectStatus(t *testing.T, what string, r resp, want int) {
	t.Helper()
	if r.status != want {
		t.Fatalf("%s: status %d, want %d\n%s", what, r.status, want, r.body)
	}
}
