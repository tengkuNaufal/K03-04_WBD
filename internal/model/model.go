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
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	IsAdmin      bool      `json:"is_admin"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
}

type Group struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"member_count"`
	LeaderID    *int64 `json:"leader_id"`
	Leader      string `json:"leader"`
}

type Database struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	DBName      string `json:"dbname"`
	Username    string `json:"username"`
	PasswordEnc []byte `json:"-"`
	SSLMode     string `json:"sslmode"`
}

type Grant struct {
	DatabaseID   int64       `json:"database_id"`
	DatabaseName string      `json:"database_name"`
	Level        authz.Level `json:"level"`
}

type DatabaseAccess struct {
	Database Database    `json:"database"`
	Level    authz.Level `json:"level"`
}

// Migration status: its position in the linear history of a database.
const (
	MigrationDraft      = "draft"       // never committed; editable
	MigrationApplied    = "applied"     // active
	MigrationRolledBack = "rolled_back" // undone by a rollback
	MigrationPartial    = "partial"     // batch run stopped half way (applied or reverted)
)

const (
	ModeNormal = "normal"
	ModeBatch  = "batch"
)

type Migration struct {
	ID                 int64     `json:"id"`
	DatabaseID         int64     `json:"database_id"`
	Name               string    `json:"name"`
	UpSQL              string    `json:"up_sql"`
	DownSQL            string    `json:"down_sql"`
	IrreversibleReason string    `json:"irreversible_reason"`
	Mode               string    `json:"mode"`
	BatchTable         string    `json:"batch_table"`
	BatchColumn        string    `json:"batch_column"`
	BatchSize          int       `json:"batch_size"`
	Status             string    `json:"status"`
	Version            *int      `json:"version"`
	CreatedBy          string    `json:"created_by"`
	CreatedAt          time.Time `json:"created_at"`
	Locked             bool      `json:"locked"` // executed at least once: SQL is immutable
}

func (m Migration) Reversible() bool { return m.IrreversibleReason == "" && m.DownSQL != "" }

// Change request actions.
const (
	ActionApply    = "apply"
	ActionRollback = "rollback"
	ActionForward  = "forward"
	ActionResume   = "resume"
)

// Change request statuses. Draft is the migration itself; a request starts at
// pending_review.
const (
	ReqPendingReview     = "pending_review"
	ReqApproved          = "approved"
	ReqRejected          = "rejected"
	ReqCancelled         = "cancelled"
	ReqQueued            = "queued"
	ReqRunning           = "running"
	ReqSuccess           = "success"
	ReqFailed            = "failed"
	ReqPartial           = "partial"            // batch up run stopped; committed batches stay
	ReqPartiallyReverted = "partially_reverted" // batch down run stopped
	ReqUncertain         = "uncertain"          // commit outcome unknown until the evidence is checked
	ReqSuperseded        = "superseded"         // a resume or rollback took over its batches
)

// Blocking statuses hold every other migration on the same database.
func Blocking(status string) bool {
	return status == ReqPartial || status == ReqPartiallyReverted || status == ReqUncertain
}

// Open statuses: the request is still on its way to the database.
func Open(status string) bool {
	return status == ReqPendingReview || status == ReqApproved || status == ReqQueued || status == ReqRunning
}

const (
	DirUp   = "up"
	DirDown = "down"
)

type Request struct {
	ID              int64      `json:"id"`
	DatabaseID      int64      `json:"database_id"`
	MigrationID     int64      `json:"migration_id"`
	MigrationName   string     `json:"migration_name"`
	Action          string     `json:"action"`
	Direction       string     `json:"direction"`
	TargetRequestID *int64     `json:"target_request_id"` // resume: the stopped request; rollback: the request that applied it
	SeriesID        int64      `json:"series_id"`         // checkpoints of this run (resume continues the target's series)
	UndoSeriesID    *int64     `json:"undo_series_id"`    // down runs: the up series being undone
	Status          string     `json:"status"`
	Note            string     `json:"note"`
	SQLHash         string     `json:"-"`
	ScopeMax        *int64     `json:"scope_max"`
	BatchesDone     int        `json:"batches_done"`
	RowsDone        int64      `json:"rows_done"`
	Error           string     `json:"error"`
	RequestedBy     string     `json:"requested_by"`
	RequestedByID   int64      `json:"requested_by_id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	JobID           *int64     `json:"job_id"`
	Approvals       []Approval `json:"approvals,omitempty"`
}

type Approval struct {
	GroupID   int64      `json:"group_id"`
	GroupName string     `json:"group_name"`
	LeaderID  *int64     `json:"leader_id"`
	Leader    string     `json:"leader"`
	Decision  string     `json:"decision"` // "", approved, rejected
	DecidedBy string     `json:"decided_by"`
	DecidedAt *time.Time `json:"decided_at"`
	Comment   string     `json:"comment"`
}

// Approved reports whether every required group approved.
func Approved(as []Approval) bool {
	for _, a := range as {
		if a.Decision != "approved" {
			return false
		}
	}
	return len(as) > 0
}

const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
)

type Job struct {
	ID           int64             `json:"id"`
	RequestID    int64             `json:"request_id"`
	DatabaseID   int64             `json:"database_id"`
	Status       string            `json:"status"`
	Attempts     int               `json:"attempts"`
	MaxAttempts  int               `json:"max_attempts"`
	RunAfter     time.Time         `json:"run_after"`
	LockedBy     string            `json:"locked_by"`
	TraceContext map[string]string `json:"-"`
	EnqueuedAt   time.Time         `json:"enqueued_at"`
	StartedAt    *time.Time        `json:"started_at"`
	FinishedAt   *time.Time        `json:"finished_at"`
}

// Execution statuses. History rows are final once they leave "running".
const (
	ExecRunning           = "running"
	ExecSuccess           = "success"
	ExecFailed            = "failed"
	ExecPartial           = "partial"
	ExecPartiallyReverted = "partially_reverted"
	ExecUncertain         = "uncertain"
)

// Execution is one attempt: the migration history row.
type Execution struct {
	ID                 int64      `json:"id"`
	RequestID          int64      `json:"request_id"`
	JobID              int64      `json:"job_id"`
	DatabaseID         int64      `json:"database_id"`
	MigrationID        int64      `json:"migration_id"`
	MigrationName      string     `json:"migration_name"`
	MigrationVersion   *int       `json:"migration_version"`
	Action             string     `json:"action"`
	Direction          string     `json:"direction"`
	Attempt            int        `json:"attempt"`
	RevertsExecutionID *int64     `json:"reverts_execution_id"`
	SQL                string     `json:"sql"`
	Status             string     `json:"status"`
	Batches            int        `json:"batches"`
	Rows               int64      `json:"rows"`
	Error              string     `json:"error"`
	ExecutedBy         string     `json:"executed_by"`
	TraceID            string     `json:"trace_id"`
	StartedAt          time.Time  `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at"`
}

type TableInfo struct {
	Schema        string `json:"schema"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	EstimatedRows int64  `json:"estimated_rows"`
}

type ColumnInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	NotNull bool   `json:"not_null"`
	Default string `json:"default"`
}

type ConstraintInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Definition string `json:"definition"`
}

type IndexInfo struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

type TableDetail struct {
	TableInfo
	Columns     []ColumnInfo     `json:"columns"`
	Constraints []ConstraintInfo `json:"constraints"`
	Indexes     []IndexInfo      `json:"indexes"`
}

type RowPage struct {
	Columns []string    `json:"columns"`
	Rows    [][]*string `json:"rows"`
	Offset  int         `json:"offset"`
	Limit   int         `json:"limit"`
	HasMore bool        `json:"has_more"`
}

type ExplainResult struct {
	Statement string `json:"statement"`
	Plan      string `json:"plan"`
	Note      string `json:"note"`
	Error     string `json:"error"`
}
