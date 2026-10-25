package model

import (
	"errors"
	"time"

	"dbmp/internal/authz"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func Invalid(msg string) error { return &ValidationError{Msg: msg} }

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	IsAdmin      bool
	IsActive     bool
	CreatedAt    time.Time
}

type Group struct {
	ID          int64
	Name        string
	MemberCount int
}

type Database struct {
	ID          int64
	Name        string
	Host        string
	Port        int
	DBName      string
	Username    string
	PasswordEnc []byte 
	SSLMode     string
}

type Grant struct {
	DatabaseID   int64
	DatabaseName string
	Level        authz.Level
}

type DatabaseAccess struct {
	Database Database
	Level    authz.Level
}

const (
	MigrationDraft   = "draft"
	MigrationApplied = "applied"
	MigrationFailed  = "failed"
)

type Migration struct {
	ID         int64
	DatabaseID int64
	Name       string
	SQL        string
	Status     string
	Version    *int 
	CreatedBy  string
	CreatedAt  time.Time
	ExecutedBy string
	ExecutedAt *time.Time
	Error      string
}

type TableInfo struct {
	Schema        string
	Name          string
	Kind          string
	EstimatedRows int64
}

type ColumnInfo struct {
	Name    string
	Type    string
	NotNull bool
	Default string
}

type ConstraintInfo struct {
	Name       string
	Type       string
	Definition string
}

type IndexInfo struct {
	Name       string
	Definition string
}

type TableDetail struct {
	TableInfo
	Columns     []ColumnInfo
	Constraints []ConstraintInfo
	Indexes     []IndexInfo
}

type RowPage struct {
	Columns []string
	Rows    [][]*string 
	Offset  int
	Limit   int
	HasMore bool
}

func (p RowPage) Prev() int { return max(p.Offset-p.Limit, 0) }

type ExplainResult struct {
	Statement string
	Plan      string
	Note      string
	Error     string
}
