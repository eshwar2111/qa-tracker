# qa-tracker — Design Spec

**Date:** 2026-10-08
**Status:** Approved design, pending spec review

## 1. Purpose

A local test-case management and bug-fix-loop tool for the Voice Agent project (and later, other projects).

The loop it exists to make fast:

1. Claude authors test cases for the whole platform and keeps them current **in a database** (never in static HTML).
2. The user executes cases manually and marks each **pass / fail / blocked**, with free-text remarks (and screenshots / log snippets) on failure.
3. On "pick up bugs", Claude reads failing cases + remarks from the DB, fixes the code, and records the fix (commit, note).
4. The user retests only what needs retesting; the case either passes (closed) or fails again (reopened, flagged as regression).

**Success criteria**
- Every state change (by user or Claude) is persisted in SQLite and visible in the UI within ~3 s without reload.
- Claude can run the full loop from the CLI without touching SQL.
- A retest pass of N fixed bugs takes the user roughly N keystrokes + remarks.
- Full history per case: who changed what, when, which commit fixed it, how often it reopened.

**Non-goals (v1):** multi-user auth, remote hosting, automated test execution, integration with GitHub issues.

## 2. Constraints & decisions

| Decision | Choice | Why |
|---|---|---|
| Location | `E:\qa-tracker`, its own git repo | Outside the voice-agent repo, per user |
| Language | Go (1.26) | Same toolchain the user already runs |
| DB | SQLite via `modernc.org/sqlite` (pure Go) | No CGO / MinGW dependency; WAL mode for concurrent server + CLI |
| UI | Vanilla HTML/CSS/JS embedded with `embed.FS`, no build step | Lightweight, single binary |
| Claude access | CLI subcommands of the same binary, sharing the `store` package (direct DB access) | Works when the server is off; one code path for business rules |
| Live updates | UI polls `GET /api/events?since=<id>` every 3 s | Simple, robust; the `event` table already exists for history |
| Port | `7777` (flag `--addr`) | |

## 3. Architecture

```
qa.exe
 ├─ cmd/qa/main.go          subcommand dispatch (serve | bugs | claim | fixed | import | case | run | area | export)
 ├─ internal/store/         ALL business rules: schema/migrations, CRUD, status transitions, events
 ├─ internal/server/        REST handlers (thin, call store) + static UI
 ├─ internal/cli/           CLI commands (thin, call store), JSON output
 └─ web/                    index.html, app.js, styles.css (embedded)
```

- **`store` is the only package that writes SQL.** Server and CLI never mutate tables directly; they call store methods such as `RecordResult`, `ClaimFix`, `MarkFixed`, `ImportCases`. Each such method runs in one transaction and appends exactly one or more `event` rows.
- **DB location:** `--db` flag › `QA_DB` env var › `qa.db` next to the executable. Attachments live in `attachments/` beside the DB.
- SQLite opened with `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`.

## 4. Data model

```sql
project(id PK, key TEXT UNIQUE, name, repo_path, created_at)

area(id PK, project_id FK, key TEXT, name, sort_order,
     UNIQUE(project_id, key))

test_case(id PK, project_id FK, area_id FK,
          key TEXT,                 -- stable slug, e.g. 'asr.whisper.basic-transcribe'
          title, preconditions, steps_json, expected,
          priority TEXT CHECK IN ('P0','P1','P2','P3'),
          tags TEXT,                -- comma-separated
          status TEXT,              -- see §5
          severity TEXT NULL,       -- set on fail: critical|major|minor|trivial
          version INT,              -- bumped when content changes
          reopen_count INT DEFAULT 0,
          archived INT DEFAULT 0,
          created_at, updated_at,
          UNIQUE(project_id, key))

case_version(id PK, case_id FK, version, title, preconditions, steps_json,
             expected, priority, created_at)          -- snapshot of each previous version

run(id PK, project_id FK, name, build TEXT,  -- commit hash / build label under test
    filter_json, created_at, closed_at NULL)

run_case(run_id FK, case_id FK, case_version INT,
         result TEXT NULL CHECK IN ('pass','fail','blocked','skip'),
         remarks TEXT, severity TEXT NULL, executed_at NULL,
         PRIMARY KEY(run_id, case_id))

attachment(id PK, case_id FK, run_id FK NULL, event_id FK NULL,
           filename, mime, size, sha256, path, created_at)

event(id PK AUTOINCREMENT, project_id FK, case_id FK NULL, run_id FK NULL,
      actor TEXT CHECK IN ('user','claude'),
      kind TEXT,      -- case_created | case_updated | case_archived | result | claimed |
                      -- fixed | reopened | run_created | run_closed | comment
      from_status, to_status,
      data_json,      -- remarks, severity, commit, files, note, build, diff summary…
      created_at)
```

`event.id` is monotonic and doubles as the live-update cursor.

## 5. Case status machine

Statuses: `untested`, `pass`, `fail`, `blocked`, `in_fix`, `fixed` (= needs retest).

| Trigger | Allowed from | To | Side effects |
|---|---|---|---|
| user records `pass` in a run | any | `pass` | event `result` |
| user records `fail` | any | `fail` | requires non-empty remarks; severity (default `major`); if from `fixed` → `reopen_count++`, extra event `reopened` |
| user records `blocked` | any | `blocked` | remarks recommended |
| user records `skip` | any | unchanged | event `result` only |
| Claude `claim` | `fail`, `blocked` | `in_fix` | event `claimed` |
| Claude `fixed` | `fail`, `blocked`, `in_fix` | `fixed` | requires `--note`; `--commit`, `--files` optional; event `fixed` |
| import changes steps/expected/preconditions | any non-`untested` | `untested` | `version++`, previous content snapshotted to `case_version`, event `case_updated` with field-level diff summary |
| import changes only title/priority/tags/area | — | unchanged | `version` unchanged, event `case_updated` |
| archive | any | unchanged, `archived=1` | event `case_archived` |

Disallowed transitions return an error naming the current status (e.g. `claim` on a passing case).
**"Needs retest"** = status `fixed` or `untested`. **"Open bugs"** = status `fail`, `blocked`, or `in_fix`.
A case whose `reopen_count ≥ 1` is shown with a **regression** badge.

## 6. Runs

- `run new --build <label> [--name] [--filter fixed|needs-retest|open|all|area:<key>|priority:<P..>]` snapshots the matching non-archived cases into `run_case` (with their current `version`). Default filter: `fixed` (Claude's fixes awaiting retest) if any exist, else `needs-retest` if any exist, else `all`.
- Recording a result writes `run_case` **and** updates `test_case.status` via the §5 rules.
- Re-recording a result in the same run overwrites `run_case` and appends a new event (history is never lost).
- A run is "complete" when every `run_case.result` is non-null; closing is manual (`run close`), and closed runs are read-only.
- A case can be marked outside any run (from case detail); this records an event with `run_id NULL`.

## 7. REST API (JSON, `/api`)

| Method & path | Purpose |
|---|---|
| `GET /projects` | list projects |
| `GET /projects/{p}/summary` | counts by status, by area×status, open bugs by severity, regressions |
| `GET /projects/{p}/areas` | list areas |
| `GET /projects/{p}/cases?status=&area=&priority=&q=&archived=` | filtered list |
| `GET /cases/{id}` | case + attachments + timeline (events) + versions |
| `POST /cases/{id}/result` | `{run_id?, result, remarks, severity}` |
| `POST /cases/{id}/comment` | `{text}` |
| `POST /cases/{id}/attachments` | multipart upload (image/text/log), optional `run_id` |
| `GET /attachments/{id}` | serve file |
| `GET /projects/{p}/runs` / `POST /projects/{p}/runs` | list / create |
| `GET /runs/{id}` | run + run_cases with case info and progress |
| `POST /runs/{id}/close` | close run |
| `GET /events?project=&since=<id>` | live-update feed (max 200) |

Errors: `{"error": "..."}` with 400 (validation / illegal transition), 404, 500.
The server binds to `127.0.0.1` only. All UI writes are recorded with `actor='user'`.

## 8. CLI (Claude's interface; `actor='claude'`)

All commands accept `--db` and `--project` (default: the single project if only one exists). Output is JSON unless `--table`.

| Command | Behavior |
|---|---|
| `qa serve [--addr 127.0.0.1:7777]` | start API + UI |
| `qa project add <key> --name --repo` | create project |
| `qa import <file.json> [--archive-missing]` | upsert areas + cases by key (§9) |
| `qa export [--out file.json]` | dump current cases in import format |
| `qa bugs [--status fail,blocked,in_fix] [--area]` | open bugs: case, steps, expected, latest remarks + severity, attachment absolute paths, reopen_count, last fix note |
| `qa case show <id\|key>` | full case + timeline |
| `qa claim <id\|key>...` | → `in_fix` |
| `qa fixed <id\|key>... --note "…" [--commit <hash>] [--files a.go,b.go]` | → `fixed` |
| `qa comment <id\|key> "…"` | add comment event |
| `qa run new / list / show <id> / close <id>` | run management |
| `qa list [--status] [--area] [--priority]` | list cases |

## 9. Import format

```json
{
  "project": "voice-agent",
  "areas": [{"key": "asr", "name": "Speech recognition", "order": 2}],
  "cases": [{
    "key": "asr.whisper.basic-transcribe",
    "area": "asr",
    "title": "Spoken command is transcribed accurately in a quiet room",
    "priority": "P0",
    "tags": ["voice", "whisper"],
    "preconditions": "Built with -tags \"whisper sqlite_fts5\"; mic connected",
    "steps": ["Say 'hey jarvis'", "Say 'open notepad'"],
    "expected": "Island shows 'open notepad'; Notepad opens within 3 s"
  }]
}
```

Upsert by `(project, key)`. New → status `untested`, event `case_created`. Existing → rules in §5. `--archive-missing` archives cases absent from the file. Import is a single transaction, and validation errors abort it entirely with a list of offending keys.

## 10. Web UI

Single-page app, hash routing, light/dark via `prefers-color-scheme`.

- **Dashboard** (`#/`): status tiles (Untested, Pass, Fail, Blocked, In fix, Needs retest), area × status grid (click → filtered list), open bugs by severity, regressions list, recent activity feed (events, with Claude's fix notes highlighted).
- **Cases** (`#/cases`): filterable, searchable table: key, title, area, priority, status badge, regression badge, last update.
- **Case detail** (`#/cases/{id}`): steps/expected, quick-mark buttons, attachments gallery, timeline (results with remarks, claims, fixes with commit + note + files, version changes with diff summary), comment box.
- **Runs** (`#/runs`, `#/runs/{id}`): create run (build label, filter), run view with progress bar and a "needs retest only / failed / all" toggle; a focused card shows the current case. Shortcuts: `J`/`K` next/prev, `P` pass, `F` fail (opens remarks drawer, focus in textarea, `Ctrl+Enter` submits), `B` blocked, `S` skip, `?` help. Pasting an image or dropping a file into the drawer uploads it as an attachment.
- **Fixed cases in a run** show Claude's latest fix note above the steps, so the user knows what changed and what to check.
- Live updates: poll `/api/events?since=` every 3 s and re-fetch the affected view; a toast shows "Claude marked 3 cases fixed".

## 11. Error handling

- Store returns typed errors (`ErrNotFound`, `ErrInvalidTransition`, `ErrValidation`), mapped to 404/400 in the server and to non-zero exit + stderr message in the CLI.
- SQLITE_BUSY is absorbed by `busy_timeout`; all multi-row writes are transactional.
- Attachments: 20 MB max, stored as `attachments/<sha256><ext>` (deduplicated); MIME is sniffed, and only images, text and PDF are served inline.
- The UI shows API errors as a toast and never silently drops an input; remarks drafts are kept in `localStorage` until submitted.

## 12. Testing

- `store` unit tests on a temp DB: every §5 transition (allowed + disallowed), reopen counting, import upsert and versioning, run snapshot filters, event emission.
- `server` tests with `httptest`: each endpoint's happy path + one error path, plus attachment upload.
- `cli` smoke test: import → run new → record fail → bugs → claim → fixed → run new (needs-retest) → record pass; assert final statuses and events.
- Manual UI check in a browser after the build.

## 13. Initial content

After the tool works, Claude surveys the voice-agent codebase (CLAUDE.md, `internal/*`, docs) and authors `suites/voice-agent.json`: ~80–150 manual cases across areas such as wake word, ASR (whisper/sherpa), barge-in/AEC, TTS, Tier-0 resolver, LLM orchestrator, trust layer/approvals, file index, apps/web/media/system/window tools, Spotify, Google, Microsoft, memory, island UI, config, and robustness/offline. The suite file is committed in the qa-tracker repo and imported with `qa import`.

## 14. Ideas (added 2026-10-08)

The user records free-text feature ideas. Claude picks them up, builds them, and the user verifies the result.

- **Tables:**
  - `idea(id, project_id, text, status, reopen_count, created_at, updated_at)`. The title is the first line of `text`.
  - `idea_attachment(id, idea_id, filename, mime, size, sha256, path, created_at)`, using the same content-addressed blob storage as case attachments.
  - `event.idea_id`, added by migration when an older DB is opened.
- **Statuses:** `new → in_progress (pick) → done (Claude built it; the user must check) → accepted (user: Works)`.
  - `new | in_progress → declined` (Claude, reason required).
  - `done | declined | accepted → new` (user reopen, remarks required, `reopen_count++`).
  - The text is editable only while `new`. Comments are allowed in any status.
- **Event kinds:** `idea_created, idea_edited, idea_picked, idea_done, idea_declined, idea_accepted, idea_reopened, idea_comment, idea_attachment`. `idea_done` data includes `note`, plus `commit`, `files` and `cases` (new test-case keys) when given.
- **API:**
  - `GET|POST /api/projects/{p}/ideas`, `GET|PUT /api/ideas/{id}`
  - `POST /api/ideas/{id}/{accept,reopen,comment,attachments}`
  - `GET /api/idea-attachments/{id}`
- **CLI:** `qa ideas [--status]` (default new + in_progress, with remarks, comments since the last reopen, attachment paths and Claude's last note); `qa idea show|add|pick|done|decline|comment`.
- **UI:**
  - An Ideas tab with a composer (paste or drop attachments, draft kept locally), status filter chips, and a thread view with Works ✓ / Not quite… / Edit / Attach.
  - A nav badge counting `done` ideas, a dashboard card, and live notices for Claude's idea events.
- **Workflow rule:** when Claude finishes an idea, it also adds manual test cases for it to the suite and lists their keys in `--cases`.
