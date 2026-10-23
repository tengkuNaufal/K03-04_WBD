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
	default:
		return "none"
	}
}

// Label is the human readable name shown in the UI.
func (l Level) Label() string {
	switch l {
	case LevelView:
		return "View Only"
	case LevelEdit:
		return "View + Edit"
	default:
		return "Tidak ada akses"
	}
}

// ParseLevel converts the stored text form into a Level.
func ParseLevel(s string) (Level, error) {
	switch s {
	case "view":
		return LevelView, nil
	case "edit":
		return LevelEdit, nil
	}
	return LevelNone, fmt.Errorf("authz: invalid access level %q", s)
}

func EffectiveLevel(levels []Level) Level {
	best := LevelNone
	for _, l := range levels {
		if l > best {
			best = l
		}
	}
	return best
}

// Action is something a principal can attempt.
type Action string

const (
	// Platform administration (Platform Admin only).
	ActionManageUsers       Action = "platform.users.manage"
	ActionManageGroups      Action = "platform.groups.manage"
	ActionManageConnections Action = "platform.connections.manage"
	ActionManageAccess      Action = "platform.access.manage"
	ActionViewAudit         Action = "platform.audit.view"

	// Actions on a single target database (granted through groups).
	ActionViewSchema       Action = "db.schema.view"
	ActionViewData         Action = "db.data.view"
	ActionViewMigrations   Action = "db.migrations.view"
	ActionCreateMigration  Action = "db.migrations.create"
	ActionEditMigration    Action = "db.migrations.edit"
	ActionDeleteMigration  Action = "db.migrations.delete"
	ActionExecuteMigration Action = "db.migrations.execute"
	ActionExplainMigration Action = "db.migrations.explain"
)

type rule struct {
	platform bool  // requires Platform Admin
	minLevel Level // required database level (database actions only)
}

var rules = map[Action]rule{
	ActionManageUsers:       {platform: true},
	ActionManageGroups:      {platform: true},
	ActionManageConnections: {platform: true},
	ActionManageAccess:      {platform: true},
	ActionViewAudit:         {platform: true},

	ActionViewSchema:       {minLevel: LevelView},
	ActionViewData:         {minLevel: LevelView},
	ActionViewMigrations:   {minLevel: LevelView},
	ActionCreateMigration:  {minLevel: LevelEdit},
	ActionEditMigration:    {minLevel: LevelEdit},
	ActionDeleteMigration:  {minLevel: LevelEdit},
	ActionExecuteMigration: {minLevel: LevelEdit},
	ActionExplainMigration: {minLevel: LevelEdit},
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

type AccessSource interface {
	GroupLevels(ctx context.Context, userID, databaseID int64) ([]Level, error)
}

type Policy struct {
	src AccessSource
}

func NewPolicy(src AccessSource) *Policy { return &Policy{src: src} }

func Allows(l Level, a Action) bool {
	r, ok := rules[a]
	if !ok || r.platform {
		return false
	}
	return l >= r.minLevel
}

func IsPlatformAction(a Action) bool {
	r, ok := rules[a]
	return ok && r.platform
}

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

func (p *Policy) Authorize(ctx context.Context, pr *Principal, a Action, databaseID int64) error {
	if pr == nil {
		return ErrUnauthenticated
	}
	r, ok := rules[a]
	if !ok {
		return fmt.Errorf("%w: unknown action %q", ErrForbidden, a)
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
	if lvl == LevelNone || lvl < r.minLevel {
		return ErrForbidden
	}
	return nil
}
