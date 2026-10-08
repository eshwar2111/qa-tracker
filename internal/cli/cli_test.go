package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qa-tracker/internal/store"
)

type env struct {
	t  *testing.T
	db string
}

func (e env) run(args ...string) (string, int) {
	e.t.Helper()
	var out, errb bytes.Buffer
	code := Run(append(args, "--db", e.db), &out, &errb)
	return out.String() + errb.String(), code
}

func (e env) ok(args ...string) string {
	e.t.Helper()
	out, code := e.run(args...)
	if code != 0 {
		e.t.Fatalf("qa %v exited %d:\n%s", args, code, out)
	}
	return out
}

// TestSmokeLoop walks the whole bug-fix cycle: import → run → user fails a
// case → Claude reads bugs, claims, fixes → user retests and it passes.
func TestSmokeLoop(t *testing.T) {
	dir := t.TempDir()
	e := env{t, filepath.Join(dir, "qa.db")}
	suite := filepath.Join(dir, "suite.json")
	os.WriteFile(suite, []byte(`{"project":"va","areas":[{"key":"asr","name":"ASR"}],
		"cases":[{"key":"asr.a","area":"asr","title":"Transcribe","priority":"P0","steps":["say hi"],"expected":"hi"},
		         {"key":"asr.b","area":"asr","title":"Noise","priority":"P1","steps":["fan","say hi"],"expected":"hi"}]}`), 0o644)

	e.ok("project", "add", "va", "--name", "Voice Agent", "--repo", `E:\Voice Agent`)
	if out := e.ok("import", suite); !strings.Contains(out, `"created": 2`) {
		t.Fatalf("import: %s", out)
	}
	var run store.Run
	json.Unmarshal([]byte(e.ok("run", "new", "--build", "abc")), &run)
	if run.Progress.Total != 2 || run.Filter != "needs-retest" {
		t.Fatalf("run: %+v", run)
	}

	// The user fails asr.a through the UI (store, actor user).
	st, err := store.Open(e.db)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.ResolveProject("")
	c, _ := st.ResolveCase(p.ID, "asr.a")
	if _, err := st.RecordResult(c.ID, store.ResultInput{RunID: &run.ID, Result: "fail", Remarks: "shows 'high' not 'hi'"}, store.ActorUser); err != nil {
		t.Fatal(err)
	}
	b, _ := st.ResolveCase(p.ID, "asr.b")
	st.RecordResult(b.ID, store.ResultInput{RunID: &run.ID, Result: "pass"}, store.ActorUser)

	out := e.ok("bugs")
	var bugs struct {
		Count    int         `json:"count"`
		RepoPath string      `json:"repo_path"`
		Bugs     []store.Bug `json:"bugs"`
	}
	json.Unmarshal([]byte(out), &bugs)
	if bugs.Count != 1 || bugs.Bugs[0].Remarks != "shows 'high' not 'hi'" || bugs.RepoPath != `E:\Voice Agent` {
		t.Fatalf("bugs: %s", out)
	}

	e.ok("claim", "asr.a")
	if out, code := e.run("fixed", "asr.a"); code == 0 || !strings.Contains(out, "note") {
		t.Fatalf("fixed without note should fail: %d %s", code, out)
	}
	// Positionals before flags must work.
	e.ok("fixed", "asr.a", "--note", "normalise homophones", "--commit", "def456", "--files", "a.go,b.go")
	e.ok("comment", "asr.a", "please", "retest", "with", "fan")

	json.Unmarshal([]byte(e.ok("run", "new", "--build", "def456")), &run)
	if run.Progress.Total != 1 || run.Filter != "fixed" {
		t.Fatalf("retest run should hold only the fixed case: %+v", run)
	}
	if _, err := st.RecordResult(c.ID, store.ResultInput{RunID: &run.ID, Result: "pass"}, store.ActorUser); err != nil {
		t.Fatal(err)
	}
	st.Close()

	var d store.CaseDetail
	json.Unmarshal([]byte(e.ok("case", "show", "asr.a")), &d)
	if d.Status != "pass" || d.LastFix == nil || d.LastFix.Commit != "def456" {
		t.Fatalf("final case: %+v", d.Case)
	}
	var kinds []string
	for _, ev := range d.Events {
		kinds = append(kinds, ev.Kind)
	}
	want := "case_created,result,claimed,fixed,comment,result"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("timeline %v, want %s", kinds, want)
	}
	if out := e.ok("bugs", "--table"); !strings.Contains(out, "0 open bug(s)") {
		t.Fatalf("bugs table: %s", out)
	}
}

func TestErrorsExitNonZero(t *testing.T) {
	e := env{t, filepath.Join(t.TempDir(), "qa.db")}
	if out, code := e.run("bugs"); code == 0 || !strings.Contains(out, "no projects") {
		t.Fatalf("bugs with no project: %d %s", code, out)
	}
	if _, code := e.run("nope"); code == 0 {
		t.Fatal("unknown command exited 0")
	}
	e.ok("project", "add", "va")
	if out, code := e.run("claim", "missing.case"); code == 0 || !strings.Contains(out, "not found") {
		t.Fatalf("claim missing: %d %s", code, out)
	}
}
