# qa-tracker

A local test-case and bug-fix-loop tracker. You run test cases by hand in the web UI; Claude reads failures from the same SQLite database, fixes the code, and records each fix. Everything is stored in `qa.db`, and none of it is static HTML.

## Run it

```
start.cmd                      # or: qa.exe serve   → http://127.0.0.1:7777
```

## The loop

1. **Test.** Go to **Runs**, start a run with the build or commit you're testing, and work through the cases with the keyboard:
   - `P` pass, `F` fail, `B` blocked, `S` skip, `J`/`K` next and previous, `?` help.
   - `F` opens a remarks drawer. Type what happened and choose a severity. Paste screenshots with Ctrl+V, or drop log files in. Submit with Ctrl+Enter.
   - Unsent remarks are kept as a draft if you close the drawer.
2. **Ask Claude to "pick up the bugs".** Claude runs `qa bugs`, which returns each failing case with its steps, your remarks, attachment paths, comments and any earlier fix. Claude claims each bug, fixes it in the repo, then runs `qa fixed <id> --note "…" --commit <sha>`.
3. **Retest.** The UI shows a toast when Claude finishes. Start a new run: the default filter, **Claude's fixes to retest**, includes only the fixed cases. Each one shows Claude's note on what changed and what to check.
   - Pass closes the case.
   - Fail reopens it, increases its reopen count and flags it as a **regression** on the dashboard.

Every action appears on the case's timeline, so you can see who did what, in which run and build, and which commit.

## CLI (for Claude)

Run `qa help` for the full list. All output is JSON unless you add `--table`.

| Command | Purpose |
|---|---|
| `qa bugs [--area x]` | open bugs, worst first |
| `qa claim <id\|key>...` | mark as being fixed |
| `qa fixed <id\|key>... --note … [--commit] [--files]` | send back for retest |
| `qa comment <id\|key> "text"` | add context to the timeline |
| `qa case show <id\|key>` | full case + timeline |
| `qa import suites/voice-agent.json [--archive-missing]` | add/update cases by key |
| `qa export --out file.json` | dump current cases |
| `qa list --status fail,blocked --table` | filtered list |
| `qa run new / list / show / close` | runs |

**Editing test cases:** edit `suites/<project>.json`, then run `qa import`. Cases are matched by `key`.
- If the steps, expected result or preconditions change, the case gets a new version (the old one is kept) and goes back to **untested**.
- If only the title, priority, tags or area change, its status stays the same.

## Build & test

```
go test ./...
go build -ldflags="-s -w" -o qa.exe ./cmd/qa
```

The SQLite driver is pure Go (`modernc.org/sqlite`), so no CGO is needed. The database is found in this order: the `--db` flag, then the `QA_DB` environment variable, then `qa.db` next to `qa.exe`. Attachments are stored in `attachments/` next to the database.
