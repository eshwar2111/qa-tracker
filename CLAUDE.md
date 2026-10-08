# CLAUDE.md — qa-tracker

Manual-QA tracker plus bug-fix loop. `internal/store` owns every rule and is the only package that writes SQL. `internal/server` (REST API and embedded `web/static` UI) and `internal/cli` are thin adapters over it. The spec is in `docs/superpowers/specs/2026-10-08-qa-tracker-design.md`.

## When the user says "pick up the bugs" (from any repo)

Use `E:\qa-tracker\qa.exe` (the DB is `E:\qa-tracker\qa.db` next to it, so no flags are needed).

1. Run `qa bugs`. Each bug comes with its steps, expected result, the user's `remarks`, `severity`, `build`, `comments`, attachment absolute paths (Read the images and logs), `reopen_count` and `last_fix`. If `reopen_count > 0`, the previous fix did not work: read `last_fix` first.
2. Run `qa claim <id>` before working on a bug, so the UI shows it as in fix.
3. Fix it in the project's repo (`repo_path` in the output), and test and commit there.
4. Run `qa fixed <id> --note "<what changed + exactly what to retest>" --commit <sha> --files a.go,b.go`. The user reads the note while retesting, so make it concrete.
5. If a bug can't be fixed, or it's really a wrong test case, use `qa comment <id> "…"` instead of `fixed`. To fix a wrong test case, edit `suites/<project>.json` and run `qa import`.

## Updating test cases

Edit `suites/voice-agent.json` and run `qa import suites/voice-agent.json`. Never edit the DB directly. Keys are stable identities: don't rename a key unless you mean "a new case". Changing steps, expected or preconditions resets the case to untested and keeps the old version.

## Dev

`go test ./...` and `go build -ldflags="-s -w" -o qa.exe ./cmd/qa`. Before rebuilding, stop any running `qa.exe serve`, because Windows locks the exe. `go mod tidy` can hang on this machine (network), so edit `go.mod` by hand if needed.
