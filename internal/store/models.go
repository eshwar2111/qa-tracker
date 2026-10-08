package store

import (
	"errors"
	"slices"
)

// Actor identifies who caused a change.
type Actor string

const (
	ActorUser   Actor = "user"
	ActorClaude Actor = "claude"
)

// Case statuses.
const (
	StatusUntested = "untested"
	StatusPass     = "pass"
	StatusFail     = "fail"
	StatusBlocked  = "blocked"
	StatusInFix    = "in_fix"
	StatusFixed    = "fixed"
)

// Run results.
const (
	ResultPass    = "pass"
	ResultFail    = "fail"
	ResultBlocked = "blocked"
	ResultSkip    = "skip"
)

var (
	Statuses   = []string{StatusUntested, StatusPass, StatusFail, StatusBlocked, StatusInFix, StatusFixed}
	Results    = []string{ResultPass, ResultFail, ResultBlocked, ResultSkip}
	Priorities = []string{"P0", "P1", "P2", "P3"}
	Severities = []string{"critical", "major", "minor", "trivial"}

	// OpenStatuses are cases with an unresolved bug.
	OpenStatuses = []string{StatusFail, StatusBlocked, StatusInFix}
	// RetestStatuses are cases the user still has to (re)run.
	RetestStatuses = []string{StatusFixed, StatusUntested}
)

var (
	ErrNotFound          = errors.New("not found")
	ErrInvalidTransition = errors.New("invalid transition")
	ErrValidation        = errors.New("validation error")
)

func valid(set []string, v string) bool { return slices.Contains(set, v) }

type Project struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	RepoPath  string `json:"repo_path"`
	CreatedAt string `json:"created_at"`
}

type Area struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	SortOrder int    `json:"order"`
}

type Case struct {
	ID            int64    `json:"id"`
	ProjectID     int64    `json:"project_id"`
	AreaID        int64    `json:"area_id"`
	AreaKey       string   `json:"area"`
	AreaName      string   `json:"area_name"`
	Key           string   `json:"key"`
	Title         string   `json:"title"`
	Preconditions string   `json:"preconditions"`
	Steps         []string `json:"steps"`
	Expected      string   `json:"expected"`
	Priority      string   `json:"priority"`
	Tags          []string `json:"tags"`
	Status        string   `json:"status"`
	Severity      string   `json:"severity,omitempty"`
	Version       int      `json:"version"`
	ReopenCount   int      `json:"reopen_count"`
	Archived      bool     `json:"archived"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

type CaseFilter struct {
	Statuses        []string
	AreaKey         string
	Priority        string
	Q               string
	IncludeArchived bool
}

type CaseVersion struct {
	Version       int      `json:"version"`
	Title         string   `json:"title"`
	Preconditions string   `json:"preconditions"`
	Steps         []string `json:"steps"`
	Expected      string   `json:"expected"`
	Priority      string   `json:"priority"`
	CreatedAt     string   `json:"created_at"`
}

type CaseDetail struct {
	Case
	Events      []Event       `json:"events"`
	Attachments []Attachment  `json:"attachments"`
	Versions    []CaseVersion `json:"versions"`
	LastFix     *FixInfo      `json:"last_fix,omitempty"`
}

type Event struct {
	ID         int64          `json:"id"`
	ProjectID  int64          `json:"project_id"`
	CaseID     *int64         `json:"case_id,omitempty"`
	RunID      *int64         `json:"run_id,omitempty"`
	Actor      Actor          `json:"actor"`
	Kind       string         `json:"kind"`
	FromStatus string         `json:"from_status,omitempty"`
	ToStatus   string         `json:"to_status,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
	CreatedAt  string         `json:"created_at"`
	CaseKey    string         `json:"case_key,omitempty"`
	CaseTitle  string         `json:"case_title,omitempty"`
}

type ResultInput struct {
	RunID    *int64 `json:"run_id"`
	Result   string `json:"result"`
	Remarks  string `json:"remarks"`
	Severity string `json:"severity"`
}

type FixInput struct {
	Note   string   `json:"note"`
	Commit string   `json:"commit"`
	Files  []string `json:"files"`
}

// FixInfo is the most recent fix Claude recorded on a case.
type FixInfo struct {
	Note      string   `json:"note"`
	Commit    string   `json:"commit,omitempty"`
	Files     []string `json:"files,omitempty"`
	CreatedAt string   `json:"created_at"`
}

type Run struct {
	ID        int64       `json:"id"`
	ProjectID int64       `json:"project_id"`
	Name      string      `json:"name"`
	Build     string      `json:"build"`
	Filter    string      `json:"filter"`
	CreatedAt string      `json:"created_at"`
	ClosedAt  string      `json:"closed_at,omitempty"`
	Progress  RunProgress `json:"progress"`
}

type RunProgress struct {
	Total   int `json:"total"`
	Done    int `json:"done"`
	Pass    int `json:"pass"`
	Fail    int `json:"fail"`
	Blocked int `json:"blocked"`
	Skip    int `json:"skip"`
}

type RunCase struct {
	Case
	CaseVersion int      `json:"case_version"`
	Result      string   `json:"result,omitempty"`
	Remarks     string   `json:"remarks,omitempty"`
	RunSeverity string   `json:"run_severity,omitempty"`
	ExecutedAt  string   `json:"executed_at,omitempty"`
	LastFix     *FixInfo `json:"last_fix,omitempty"`
}

type RunDetail struct {
	Run
	Cases []RunCase `json:"cases"`
}

type Attachment struct {
	ID        int64  `json:"id"`
	CaseID    int64  `json:"case_id"`
	RunID     *int64 `json:"run_id,omitempty"`
	Filename  string `json:"filename"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
}

type AreaCount struct {
	Key      string         `json:"key"`
	Name     string         `json:"name"`
	ByStatus map[string]int `json:"by_status"`
	Total    int            `json:"total"`
}

type Summary struct {
	Total       int            `json:"total"`
	ByStatus    map[string]int `json:"by_status"`
	NeedsRetest int            `json:"needs_retest"`
	OpenBugs    int            `json:"open_bugs"`
	BySeverity  map[string]int `json:"open_by_severity"`
	Areas       []AreaCount    `json:"areas"`
	Regressions []Case         `json:"regressions"`
	Recent      []Event        `json:"recent"`
}

type Bug struct {
	ID            int64    `json:"id"`
	Key           string   `json:"key"`
	Area          string   `json:"area"`
	Title         string   `json:"title"`
	Priority      string   `json:"priority"`
	Status        string   `json:"status"`
	Severity      string   `json:"severity,omitempty"`
	ReopenCount   int      `json:"reopen_count"`
	Preconditions string   `json:"preconditions"`
	Steps         []string `json:"steps"`
	Expected      string   `json:"expected"`
	Remarks       string   `json:"remarks"`
	ReportedAt    string   `json:"reported_at,omitempty"`
	Build         string   `json:"build,omitempty"`
	Comments      []string `json:"comments,omitempty"`
	Attachments   []string `json:"attachments,omitempty"`
	LastFix       *FixInfo `json:"last_fix,omitempty"`
}

// ImportFile is the suite JSON format (spec §9); Export produces the same shape.
type ImportFile struct {
	Project string         `json:"project"`
	Areas   []ImportArea   `json:"areas"`
	Cases   []ImportCase   `json:"cases"`
}

type ImportArea struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Order int    `json:"order"`
}

type ImportCase struct {
	Key           string   `json:"key"`
	Area          string   `json:"area"`
	Title         string   `json:"title"`
	Priority      string   `json:"priority"`
	Tags          []string `json:"tags"`
	Preconditions string   `json:"preconditions"`
	Steps         []string `json:"steps"`
	Expected      string   `json:"expected"`
}

type ImportResult struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Reset     int `json:"reset_to_untested"`
	Unchanged int `json:"unchanged"`
	Archived  int `json:"archived"`
}
