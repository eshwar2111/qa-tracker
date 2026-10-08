# qa-tracker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `qa.exe`, a single Go binary that serves a test-case/bug-loop web UI and exposes a CLI for Claude, with all state in SQLite.

**Architecture:** `internal/store` holds every business rule and is the only package that writes SQL. `internal/server` (REST + embedded vanilla-JS UI) and `internal/cli` are thin adapters over the store, and `cmd/qa` dispatches subcommands.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure Go, no CGO), `net/http` pattern routing, `embed`, and vanilla HTML/CSS/JS.

**Spec:** `docs/superpowers/specs/2026-10-08-qa-tracker-design.md`

## Global Constraints

- Module path `qa-tracker`; binary `qa.exe`; repo at `E:\qa-tracker`.
- SQLite pragmas: `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`.
- DB path resolution: `--db` flag › `QA_DB` env var › `qa.db` next to the executable; attachments in `<dbdir>/attachments/`.
- Server binds `127.0.0.1:7777` by default; UI writes use actor `user`, CLI writes use actor `claude`.
- Statuses: `untested, pass, fail, blocked, in_fix, fixed`. Results: `pass, fail, blocked, skip`. Priorities: `P0..P3`. Severities: `critical, major, minor, trivial`.
- Timestamps are stored as RFC3339 UTC text.
- Attachments: max 20 MB, stored as `attachments/<sha256><ext>`.
- Every mutating store method runs in one transaction and appends ≥1 `event` row.
- CLI output is JSON by default; `--table` gives human output.

## Review Focus

1. **Server and CLI writing at the same time** — two `Store` handles on one file must both commit without `SQLITE_BUSY` errors (Task 2 test `TestConcurrentHandles`).
2. **A bad import file** — an unknown area, a bad priority or a duplicate key must abort the whole import with nothing written, listing the bad keys (Task 3 test `TestImportValidationAtomic`).
3. **Writing to a closed run, or a case not in the run** — rejected with `ErrValidation` and no status change (Task 5 test `TestResultRunGuards`).
4. **Fail with empty or whitespace-only remarks** — rejected; Unicode and multi-KB remarks round-trip intact (Task 4 test `TestFailRemarks`).
5. **More than 200 events since the cursor** — returned ascending in pages of 200, so the UI catches up with no gaps (Task 2 test `TestEventsPaging`).

---

## File structure

```
go.mod
cmd/qa/main.go                 -> calls cli.Run(os.Args[1:], os.Stdout, os.Stderr)
internal/store/db.go           Open, schema, migrate, tx helper, now()
internal/store/models.go       types + constants + errors
internal/store/events.go       addEvent, EventsSince
internal/store/projects.go     projects + areas
internal/store/cases.go        ListCases, GetCase, ResolveCase, CaseDetail
internal/store/importer.go     Import, Export, validation, diff
internal/store/transitions.go  RecordResult, Claim, MarkFixed, Comment
internal/store/runs.go         CreateRun, ListRuns, GetRun, CloseRun
internal/store/attachments.go  AddAttachment, GetAttachment
internal/store/summary.go      Summary, Bugs
internal/store/*_test.go
internal/server/server.go      routes, JSON helpers, error mapping, static
internal/server/server_test.go
internal/cli/cli.go            Run, subcommands, interspersed flag parsing
internal/cli/cli_test.go       end-to-end smoke loop
web/web.go                     //go:embed static
web/static/index.html, app.js, styles.css
```

## Store interface (produced by Tasks 2-6, consumed by Tasks 7-9)

```go
type Actor string // ActorUser="user", ActorClaude="claude"
var ErrNotFound, ErrInvalidTransition, ErrValidation error

func Open(path string) (*Store, error); func (s *Store) Close() error
func (s *Store) CreateProject(key, name, repo string) (*Project, error)
func (s *Store) ListProjects() ([]Project, error)
func (s *Store) ResolveProject(key string) (*Project, error)          // "" => the only project
func (s *Store) ListAreas(projectID int64) ([]Area, error)
func (s *Store) ListCases(projectID int64, f CaseFilter) ([]Case, error)
func (s *Store) GetCase(id int64) (*Case, error)
func (s *Store) ResolveCase(projectID int64, ref string) (*Case, error) // id or key
func (s *Store) CaseDetail(id int64) (*CaseDetail, error)
func (s *Store) Import(f ImportFile, archiveMissing bool, a Actor) (*ImportResult, error)
func (s *Store) Export(projectID int64) (*ImportFile, error)
func (s *Store) RecordResult(caseID int64, in ResultInput, a Actor) (*Case, error)
func (s *Store) Claim(caseID int64, a Actor) (*Case, error)
func (s *Store) MarkFixed(caseID int64, in FixInput, a Actor) (*Case, error)
func (s *Store) Comment(caseID int64, text string, a Actor) error
func (s *Store) CreateRun(projectID int64, name, build, filter string, a Actor) (*Run, error)
func (s *Store) ListRuns(projectID int64) ([]Run, error)
func (s *Store) GetRun(id int64) (*RunDetail, error)
func (s *Store) CloseRun(id int64, a Actor) error
func (s *Store) AddAttachment(caseID int64, runID *int64, filename string, r io.Reader, a Actor) (*Attachment, error)
func (s *Store) GetAttachment(id int64) (*Attachment, error)
func (s *Store) EventsSince(projectID, since int64, limit int) ([]Event, error)
func (s *Store) Summary(projectID int64) (*Summary, error)
func (s *Store) Bugs(projectID int64, statuses []string, areaKey string) ([]Bug, error)
```

---

### Task 1: Module scaffold
- [ ] Run `go mod init qa-tracker` and `go get modernc.org/sqlite`; add a `cmd/qa/main.go` stub that compiles.
- [ ] Verify with `go build ./...`, then commit.

### Task 2: Store core — schema, events, projects/areas
**Files:** `db.go, models.go, events.go, projects.go, store_test.go`
- [ ] Tests: `TestOpenCreatesSchema` (tables exist, WAL on); `TestResolveProject` ("" picks the only project, errors if 0 or >1); `TestConcurrentHandles` (two `Open` on the same file, 50 interleaved CreateProject/Comment writes, no error); `TestEventsPaging` (450 events → three pages 200/200/50, ascending, ids > since).
- [ ] Implement the schema from spec §4 with `CREATE TABLE IF NOT EXISTS`; DSN `file:<path>?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)`; `SetMaxOpenConns(1)` per handle.
- [ ] Run `go test ./internal/store/`, then commit.

### Task 3: Cases + import/export
**Files:** `cases.go, importer.go, import_test.go`
- [ ] Tests: `TestImportCreates` (status untested, a `case_created` event per case, areas upserted); `TestImportContentChangeResetsStatus` (pass case + changed steps → untested, version 2, `case_version` row, event diff lists `steps`); `TestImportMetaChangeKeepsStatus` (title/priority change → status kept, version kept); `TestImportArchiveMissing`; `TestImportValidationAtomic` (unknown area, priority `P9` and a duplicate key → error listing all 3 keys, zero cases in DB); `TestExportRoundTrip`; `TestResolveCase` (by id and by key).
- [ ] Implement it, run the tests, then commit.

### Task 4: Status transitions
**Files:** `transitions.go, transitions_test.go`
- [ ] Tests, table-driven over spec §5: pass/fail/blocked/skip from each status; claim allowed only from fail/blocked; fixed allowed from fail/blocked/in_fix and requires a note; fail after fixed → `reopen_count=1` plus a `reopened` event; illegal transitions → `ErrInvalidTransition` with the current status in the message. `TestFailRemarks` (empty or whitespace → `ErrValidation`; a 5 KB Unicode remark round-trips; severity defaults to major, and an invalid severity → ErrValidation); a pass clears severity.
- [ ] Implement it, run the tests, then commit.

### Task 5: Runs
**Files:** `runs.go, runs_test.go`
- [ ] Tests: filter snapshots (`needs-retest`, `open`, `all`, `area:x`, `priority:P0`, and default ""); `GetRun` progress counts; a RecordResult with a run writes `run_case` and overwrites a re-record, with 2 events; `TestResultRunGuards` (closed run → ErrValidation, case not in run → ErrValidation, status unchanged); a closed run can't be closed twice.
- [ ] Implement it, run the tests, then commit.

### Task 6: Attachments, summary, bugs, case detail
**Files:** `attachments.go, summary.go, misc_test.go`
- [ ] Tests: attachment stored at sha path, deduplicated, size > 20 MB rejected, `attachment` event; Summary counts by status and area; Bugs returns latest remarks, severity, absolute attachment paths and last fix note; CaseDetail includes events in order, plus versions and attachments.
- [ ] Implement it, run the tests, then commit.

### Task 7: HTTP server
**Files:** `internal/server/server.go, server_test.go`, `web/web.go` (placeholder index)
- [ ] Tests with `httptest`: GET summary; list cases with filter; POST result (fail without remarks → 400); POST run → GET run; multipart attachment upload → GET it back with the same bytes; events since; unknown case → 404; `/` serves index.html.
- [ ] Implement the routes from spec §7, then commit.

### Task 8: CLI
**Files:** `internal/cli/cli.go, cli_test.go, cmd/qa/main.go`
- [ ] Interspersed flag parsing (positionals may precede flags). Commands: serve, project add, import, export, bugs, case show, claim, fixed, comment, run new/list/show/close, list.
- [ ] `TestSmokeLoop` against a temp DB: project add → import → run new → RecordResult fail (as user, via store) → `bugs` JSON contains the remark → `claim` → `fixed --note --commit` → `run new` (default filter picks it up) → pass → final status pass, timeline kinds in order.
- [ ] Commit.

### Task 9: Web UI
**Files:** `web/static/index.html, app.js, styles.css`
- [ ] Views: dashboard, cases, case detail, runs, and the run player with keyboard shortcuts J/K/P/F/B/S/?, a fail drawer (Ctrl+Enter submits, paste/drop attaches, localStorage draft), the fix note shown on fixed cases, a 3 s event poll with a toast, and light/dark tokens.
- [ ] Manual check: `qa serve`, open it in a browser, and walk through the loop.
- [ ] Commit.

### Task 10: Voice-agent suite
- [ ] Survey the voice-agent repo; write `suites/voice-agent.json` (~80–150 cases); `qa project add voice-agent`; `qa import`; add a README with the workflow. Commit.
