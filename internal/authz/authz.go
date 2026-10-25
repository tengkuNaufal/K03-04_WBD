// Package authz holds every authorization rule of the platform in one place.
package authz

import (
	"context"
	"errors"
	"fmt"
)

// Level is the access level a group has on a target database.
type Level int

const (
	LevelNone Level = iota
	LevelView       // View Only
	LevelEdit       // View + Edit
)

func (l Level) String() string {
	switch l {
	case LevelView:
		return "view"
	case LevelEdit:
		return "edit"
	}
	return "none"
}

// Label is the name shown in the UI.
func (l Level) Label() string {
	switch l {
	case LevelView:
		return "View Only"
	case LevelEdit:
		return "View + Edit"
	}
	return "Tidak ada akses"
}

func ParseLevel(s string) (Level, error) {
	switch s {
	case "view":
		return LevelView, nil
	case "edit":
		return LevelEdit, nil
	}
	return LevelNone, fmt.Errorf("authz: invalid access level %q", s)
}

// EffectiveLevel is the access of a user that belongs to several groups:
// the most permissive level wins.
func EffectiveLevel(levels []Level) Level {
	best := LevelNone
	for _, l := range levels {
		best = max(best, l)
	}
	return best
}

type Action string

const (
	// ActionManagePlatform covers users, groups, connections and grants.
	ActionManagePlatform Action = "platform.manage"
	// ActionView covers schema, data and migration history of a database.
	ActionView Action = "db.view"
	// ActionEdit covers creating, editing, explaining and executing migrations.
	ActionEdit Action = "db.edit"
)

type rule struct {
	platform bool  // only Platform Admin
	minLevel Level // database actions: required level
}

// rules is the single table of authorization rules.
var rules = map[Action]rule{
	ActionManagePlatform: {platform: true},
	ActionView:           {minLevel: LevelView},
	ActionEdit:           {minLevel: LevelEdit},
}

var (
	ErrUnauthenticated = errors.New("authz: not authenticated")
	ErrForbidden       = errors.New("authz: forbidden")
)

type Principal struct {
	UserID   int64
	Username string
	IsAdmin  bool
}

// AccessSource returns the levels of every group of a user on a database.
type AccessSource interface {
	GroupLevels(ctx context.Context, userID, databaseID int64) ([]Level, error)
}

type Policy struct{ src AccessSource }

func NewPolicy(src AccessSource) *Policy { return &Policy{src: src} }

// Allows reports whether a database level permits a database action.
func Allows(l Level, a Action) bool {
	r, ok := rules[a]
	return ok && !r.platform && l >= r.minLevel && l > LevelNone
}

// Level returns the effective level of a user on a database.
func (p *Policy) Level(ctx context.Context, pr *Principal, databaseID int64) (Level, error) {
	if pr == nil {
		return LevelNone, ErrUnauthenticated
	}
	levels, err := p.src.GroupLevels(ctx, pr.UserID, databaseID)
	if err != nil {
		return LevelNone, fmt.Errorf("authz: load group levels: %w", err)
	}
	return EffectiveLevel(levels), nil
}

// Authorize checks an action. Platform Admin is not a database level: an
// admin still needs a group to see a database.
func (p *Policy) Authorize(ctx context.Context, pr *Principal, a Action, databaseID int64) error {
	if pr == nil {
		return ErrUnauthenticated
	}
	r, ok := rules[a]
	if !ok {
		return ErrForbidden
	}
	if r.platform {
		if pr.IsAdmin {
			return nil
		}
		return ErrForbidden
	}
	lvl, err := p.Level(ctx, pr, databaseID)
	if err != nil {
		return err
	}
	if !Allows(lvl, a) {
		return ErrForbidden
	}
	return nil
}
