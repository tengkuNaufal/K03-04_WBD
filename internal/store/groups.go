package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/authz"
	"dbmp/internal/model"
)

const groupCols = `g.id, g.name, (SELECT count(*) FROM group_members m WHERE m.group_id = g.id)`

func scanGroup(row pgx.Row) (model.Group, error) {
	var g model.Group
	err := row.Scan(&g.ID, &g.Name, &g.MemberCount)
	return g, mapErr(err)
}

func (s *Store) CreateGroup(ctx context.Context, name string) (int64, error) {
	return s.insert(ctx, `INSERT INTO groups (name) VALUES ($1) RETURNING id`, name)
}

func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	return s.exec(ctx, `DELETE FROM groups WHERE id = $1`, id)
}

func (s *Store) GetGroup(ctx context.Context, id int64) (model.Group, error) {
	return scanGroup(s.pool.QueryRow(ctx, `SELECT `+groupCols+` FROM groups g WHERE g.id = $1`, id))
}

func (s *Store) ListGroups(ctx context.Context) ([]model.Group, error) {
	rows, _ := s.pool.Query(ctx, `SELECT `+groupCols+` FROM groups g ORDER BY g.name`)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Group, error) { return scanGroup(r) })
}

func (s *Store) AddMember(ctx context.Context, groupID, userID int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO group_members (group_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, groupID, userID)
	return mapErr(err)
}

func (s *Store) RemoveMember(ctx context.Context, groupID, userID int64) error {
	return s.exec(ctx, `DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, groupID, userID)
}

func (s *Store) ListMembers(ctx context.Context, groupID int64) ([]model.User, error) {
	rows, _ := s.pool.Query(ctx, `SELECT `+userCols+` FROM users
		WHERE id IN (SELECT user_id FROM group_members WHERE group_id = $1) ORDER BY username`, groupID)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.User, error) { return scanUser(r) })
}

// GetGrant returns the level of a group on a database (LevelNone if none).
func (s *Store) GetGrant(ctx context.Context, groupID, databaseID int64) (authz.Level, error) {
	var lvl string
	err := s.pool.QueryRow(ctx,
		`SELECT level FROM group_database_access WHERE group_id = $1 AND database_id = $2`, groupID, databaseID).Scan(&lvl)
	if err == pgx.ErrNoRows {
		return authz.LevelNone, nil
	}
	if err != nil {
		return authz.LevelNone, err
	}
	return authz.ParseLevel(lvl)
}

func (s *Store) SetGrant(ctx context.Context, groupID, databaseID int64, level authz.Level) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO group_database_access (group_id, database_id, level) VALUES ($1, $2, $3)
		ON CONFLICT (group_id, database_id) DO UPDATE SET level = EXCLUDED.level`,
		groupID, databaseID, level.String())
	return mapErr(err)
}

func (s *Store) RemoveGrant(ctx context.Context, groupID, databaseID int64) error {
	return s.exec(ctx, `DELETE FROM group_database_access WHERE group_id = $1 AND database_id = $2`, groupID, databaseID)
}

func (s *Store) ListGrants(ctx context.Context, groupID int64) ([]model.Grant, error) {
	rows, _ := s.pool.Query(ctx, `
		SELECT a.database_id, d.name, a.level FROM group_database_access a
		JOIN databases d ON d.id = a.database_id WHERE a.group_id = $1 ORDER BY d.name`, groupID)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Grant, error) {
		var g model.Grant
		var lvl string
		if err := r.Scan(&g.DatabaseID, &g.DatabaseName, &lvl); err != nil {
			return g, err
		}
		g.Level, _ = authz.ParseLevel(lvl)
		return g, nil
	})
}

// GroupLevels implements authz.AccessSource. Inactive users get nothing.
func (s *Store) GroupLevels(ctx context.Context, userID, databaseID int64) ([]authz.Level, error) {
	rows, _ := s.pool.Query(ctx, `
		SELECT a.level FROM group_database_access a
		JOIN group_members m ON m.group_id = a.group_id
		JOIN users u ON u.id = m.user_id
		WHERE m.user_id = $1 AND a.database_id = $2 AND u.is_active`, userID, databaseID)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (authz.Level, error) {
		var lvl string
		if err := r.Scan(&lvl); err != nil {
			return authz.LevelNone, err
		}
		return authz.ParseLevel(lvl)
	})
}

// AccessibleDatabases lists the databases a user can reach with the
// effective level (authz.EffectiveLevel) of all the user's groups.
func (s *Store) AccessibleDatabases(ctx context.Context, userID int64) ([]model.DatabaseAccess, error) {
	rows, _ := s.pool.Query(ctx, `
		SELECT `+dbCols+`, array_agg(a.level) FROM databases d
		JOIN group_database_access a ON a.database_id = d.id
		JOIN group_members m ON m.group_id = a.group_id
		WHERE m.user_id = $1 GROUP BY d.id ORDER BY d.name`, userID)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.DatabaseAccess, error) {
		var a model.DatabaseAccess
		var raw []string
		d := &a.Database
		if err := r.Scan(&d.ID, &d.Name, &d.Host, &d.Port, &d.DBName, &d.Username, &d.PasswordEnc, &d.SSLMode, &raw); err != nil {
			return a, err
		}
		var levels []authz.Level
		for _, l := range raw {
			lvl, _ := authz.ParseLevel(l)
			levels = append(levels, lvl)
		}
		a.Level = authz.EffectiveLevel(levels)
		return a, nil
	})
}
