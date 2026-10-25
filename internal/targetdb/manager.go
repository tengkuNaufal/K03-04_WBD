package targetdb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"dbmp/internal/model"
	"dbmp/internal/secret"
)

const (
	QueryTimeout = 10 * time.Second // viewer queries
	ExecTimeout  = 60 * time.Second // migrations and EXPLAIN
)

type ConnError struct{ Err error }

func (e *ConnError) Error() string { return "koneksi ke database target gagal: " + summarize(e.Err) }
func (e *ConnError) Unwrap() error { return e.Err }

func IsConnError(err error) bool {
	var ce *ConnError
	return errors.As(err, &ce)
}

// Params are the plain connection settings of a target database.
type Params struct {
	Host, DBName, Username, Password, SSLMode string
	Port                                      int
}

func (p Params) connString() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(p.Username, p.Password),
		Host:     net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
		Path:     "/" + p.DBName,
		RawQuery: url.Values{"sslmode": {p.SSLMode}, "connect_timeout": {"5"}}.Encode(),
	}
	return u.String()
}

// Test opens one connection with the given settings.
func Test(ctx context.Context, p Params) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, p.connString())
	if err != nil {
		return &ConnError{Err: err}
	}
	return conn.Close(ctx)
}

type Lookup interface {
	GetDatabase(ctx context.Context, id int64) (model.Database, error)
}

// Manager keeps one connection pool per target database.
type Manager struct {
	lookup Lookup
	box    *secret.Box
	mu     sync.Mutex
	pools  map[int64]*pgxpool.Pool
}

func NewManager(lookup Lookup, box *secret.Box) *Manager {
	return &Manager{lookup: lookup, box: box, pools: map[int64]*pgxpool.Pool{}}
}

func (m *Manager) pool(ctx context.Context, id int64) (*pgxpool.Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[id]; ok {
		return p, nil
	}
	d, err := m.lookup.GetDatabase(ctx, id)
	if err != nil {
		return nil, err
	}
	pw, err := m.box.Decrypt(d.PasswordEnc)
	if err != nil {
		return nil, fmt.Errorf("targetdb: decrypt password: %w", err)
	}
	params := Params{Host: d.Host, Port: d.Port, DBName: d.DBName, Username: d.Username, Password: string(pw), SSLMode: d.SSLMode}
	cfg, err := pgxpool.ParseConfig(params.connString())
	if err != nil {
		return nil, &ConnError{Err: err}
	}
	cfg.MaxConns = 5
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, &ConnError{Err: err}
	}
	m.pools[id] = p
	return p, nil
}

// Invalidate closes the pool of a database whose settings changed.
func (m *Manager) Invalidate(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[id]; ok {
		p.Close()
		delete(m.pools, id)
	}
}

func (m *Manager) Close() {
	for id := range m.pools {
		m.Invalidate(id)
	}
}

// begin starts a transaction with a statement timeout.
func (m *Manager) begin(ctx context.Context, id int64, opts pgx.TxOptions, timeout time.Duration) (pgx.Tx, func(), error) {
	p, err := m.pool(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	conn, err := p.Acquire(ctx)
	if err != nil {
		return nil, nil, &ConnError{Err: err}
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		conn.Release()
		return nil, nil, &ConnError{Err: err}
	}
	done := func() {
		_ = tx.Rollback(context.Background())
		conn.Release()
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", timeout.Milliseconds())); err != nil {
		done()
		return nil, nil, err
	}
	return tx, done, nil
}

func summarize(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Message
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) && ce.Unwrap() != nil {
		return summarize(ce.Unwrap())
	}
	return err.Error()
}

// PgErrorMessage formats a PostgreSQL error for display.
func PgErrorMessage(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err.Error()
	}
	msg := fmt.Sprintf("%s (SQLSTATE %s)", pgErr.Message, pgErr.Code)
	if pgErr.Detail != "" {
		msg += " — " + pgErr.Detail
	}
	return msg
}
