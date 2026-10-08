package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qa.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func suite() ImportFile {
	return ImportFile{
		Project: "va",
		Areas:   []ImportArea{{Key: "asr", Name: "ASR", Order: 1}, {Key: "ui", Name: "UI", Order: 2}},
		Cases: []ImportCase{
			{Key: "asr.basic", Area: "asr", Title: "Basic", Priority: "P0", Tags: []string{"voice"}, Steps: []string{"say hi"}, Expected: "hi shown"},
			{Key: "asr.noise", Area: "asr", Title: "Noise", Priority: "P1", Steps: []string{"fan on", "say hi"}, Expected: "hi shown"},
			{Key: "ui.idle", Area: "ui", Title: "Idle", Priority: "P2", Steps: []string{"wait"}, Expected: "island idle"},
		},
	}
}

// seeded returns a store with project "va" and the 3-case suite imported.
func seeded(t *testing.T) (*Store, *Project) {
	s, _ := newStore(t)
	p := must[*Project](t)(s.CreateProject("va", "Voice Agent", ""))
	must[*ImportResult](t)(s.Import(suite(), false, ActorClaude))
	return s, p
}

func caseByKey(t *testing.T, s *Store, p *Project, key string) *Case {
	t.Helper()
	return must[*Case](t)(s.ResolveCase(p.ID, key))
}

func fail(t *testing.T, s *Store, c *Case) *Case {
	t.Helper()
	return must[*Case](t)(s.RecordResult(c.ID, ResultInput{Result: ResultFail, Remarks: "broken"}, ActorUser))
}

func TestOpenCreatesSchema(t *testing.T) {
	s, _ := newStore(t)
	for _, tbl := range []string{"project", "area", "test_case", "case_version", "run", "run_case", "event", "attachment"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing (n=%d err=%v)", tbl, n, err)
		}
	}
	var mode string
	s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestResolveProject(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.ResolveProject(""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no projects: got %v", err)
	}
	must[*Project](t)(s.CreateProject("a", "", ""))
	if p, err := s.ResolveProject(""); err != nil || p.Key != "a" {
		t.Fatalf("single project: %v %v", p, err)
	}
	must[*Project](t)(s.CreateProject("b", "", ""))
	if _, err := s.ResolveProject(""); !errors.Is(err, ErrValidation) {
		t.Fatalf("two projects: got %v", err)
	}
	if p, err := s.ResolveProject("b"); err != nil || p.Key != "b" {
		t.Fatalf("by key: %v %v", p, err)
	}
	if _, err := s.CreateProject("a", "", ""); !errors.Is(err, ErrValidation) {
		t.Fatalf("duplicate project: %v", err)
	}
}

// Review focus 1: server and CLI hold separate handles on one file.
func TestConcurrentHandles(t *testing.T) {
	s1, path := newStore(t)
	p := must[*Project](t)(s1.CreateProject("va", "", ""))
	must[*ImportResult](t)(s1.Import(suite(), false, ActorClaude))
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	c := caseByKey(t, s1, p, "asr.basic")
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); errs <- s1.Comment(c.ID, "from server", ActorUser) }()
		go func() { defer wg.Done(); errs <- s2.Comment(c.ID, "from cli", ActorClaude) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write failed: %v", err)
		}
	}
	d := must[*CaseDetail](t)(s1.CaseDetail(c.ID))
	if n := countKind(d.Events, "comment"); n != 100 {
		t.Fatalf("comments = %d, want 100", n)
	}
}

func countKind(evs []Event, kind string) int {
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// Review focus 5: catching up through more than one page of events.
func TestEventsPaging(t *testing.T) {
	s, p := seeded(t)
	c := caseByKey(t, s, p, "asr.basic")
	for i := 0; i < 450; i++ {
		if err := s.Comment(c.ID, "x", ActorUser); err != nil {
			t.Fatal(err)
		}
	}
	var since int64
	var sizes []int
	total := 0
	for {
		evs := must[[]Event](t)(s.EventsSince(p.ID, since, 200))
		if len(evs) == 0 {
			break
		}
		for _, e := range evs {
			if e.ID <= since {
				t.Fatalf("event %d not after cursor %d", e.ID, since)
			}
			since = e.ID
		}
		sizes = append(sizes, len(evs))
		total += len(evs)
	}
	// 1 project_created + 3 case_created + 450 comments
	if total != 454 || sizes[0] != 200 || sizes[1] != 200 || sizes[2] != 54 {
		t.Fatalf("pages %v total %d", sizes, total)
	}
}

func TestImportCreates(t *testing.T) {
	s, p := seeded(t)
	cases := must[[]Case](t)(s.ListCases(p.ID, CaseFilter{}))
	if len(cases) != 3 {
		t.Fatalf("cases = %d", len(cases))
	}
	for _, c := range cases {
		if c.Status != StatusUntested || c.Version != 1 {
			t.Fatalf("%s: status %s version %d", c.Key, c.Status, c.Version)
		}
	}
	if a := must[[]Area](t)(s.ListAreas(p.ID)); len(a) != 2 {
		t.Fatalf("areas = %d", len(a))
	}
	evs := must[[]Event](t)(s.EventsSince(p.ID, 0, 200))
	if countKind(evs, "case_created") != 3 {
		t.Fatalf("case_created events = %d", countKind(evs, "case_created"))
	}
	// Re-import is a no-op.
	r := must[*ImportResult](t)(s.Import(suite(), false, ActorClaude))
	if r.Unchanged != 3 || r.Created+r.Updated != 0 {
		t.Fatalf("reimport %+v", r)
	}
}

func TestImportContentChangeResetsStatus(t *testing.T) {
	s, p := seeded(t)
	c := caseByKey(t, s, p, "asr.basic")
	must[*Case](t)(s.RecordResult(c.ID, ResultInput{Result: ResultPass}, ActorUser))
	f := suite()
	f.Cases[0].Steps = []string{"say hello"}
	r := must[*ImportResult](t)(s.Import(f, false, ActorClaude))
	if r.Updated != 1 || r.Reset != 1 {
		t.Fatalf("result %+v", r)
	}
	d := must[*CaseDetail](t)(s.CaseDetail(c.ID))
	if d.Status != StatusUntested || d.Version != 2 || len(d.Versions) != 1 || d.Versions[0].Steps[0] != "say hi" {
		t.Fatalf("status %s version %d versions %+v", d.Status, d.Version, d.Versions)
	}
	last := d.Events[len(d.Events)-1]
	if last.Kind != "case_updated" || !strings.Contains(strings.Join(anyStrings(last.Data["changed"]), ","), "steps") {
		t.Fatalf("last event %+v", last)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestImportMetaChangeKeepsStatus(t *testing.T) {
	s, p := seeded(t)
	c := caseByKey(t, s, p, "asr.basic")
	must[*Case](t)(s.RecordResult(c.ID, ResultInput{Result: ResultPass}, ActorUser))
	f := suite()
	f.Cases[0].Title = "Basic transcription"
	f.Cases[0].Priority = "P1"
	must[*ImportResult](t)(s.Import(f, false, ActorClaude))
	c = caseByKey(t, s, p, "asr.basic")
	if c.Status != StatusPass || c.Version != 1 || c.Title != "Basic transcription" || c.Priority != "P1" {
		t.Fatalf("%+v", c)
	}
}

func TestImportArchiveMissing(t *testing.T) {
	s, p := seeded(t)
	f := suite()
	f.Cases = f.Cases[:2]
	r := must[*ImportResult](t)(s.Import(f, true, ActorClaude))
	if r.Archived != 1 {
		t.Fatalf("archived = %d", r.Archived)
	}
	if n := len(must[[]Case](t)(s.ListCases(p.ID, CaseFilter{}))); n != 2 {
		t.Fatalf("active cases = %d", n)
	}
	// Re-adding unarchives it.
	must[*ImportResult](t)(s.Import(suite(), false, ActorClaude))
	if n := len(must[[]Case](t)(s.ListCases(p.ID, CaseFilter{}))); n != 3 {
		t.Fatalf("after unarchive = %d", n)
	}
}

// Review focus 2: a bad file writes nothing.
func TestImportValidationAtomic(t *testing.T) {
	s, _ := newStore(t)
	p := must[*Project](t)(s.CreateProject("va", "", ""))
	f := suite()
	f.Cases = append(f.Cases,
		ImportCase{Key: "x.unknown", Area: "nope", Title: "t", Priority: "P0", Steps: []string{"s"}, Expected: "e"},
		ImportCase{Key: "x.prio", Area: "asr", Title: "t", Priority: "P9", Steps: []string{"s"}, Expected: "e"},
		ImportCase{Key: "asr.basic", Area: "asr", Title: "dup", Priority: "P0", Steps: []string{"s"}, Expected: "e"},
	)
	_, err := s.Import(f, false, ActorClaude)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v", err)
	}
	for _, k := range []string{"x.unknown", "x.prio", "asr.basic"} {
		if !strings.Contains(err.Error(), k) {
			t.Fatalf("error does not name %s: %v", k, err)
		}
	}
	if n := len(must[[]Case](t)(s.ListCases(p.ID, CaseFilter{}))); n != 0 {
		t.Fatalf("cases written = %d", n)
	}
	if n := len(must[[]Area](t)(s.ListAreas(p.ID))); n != 0 {
		t.Fatalf("areas written = %d", n)
	}
}

func TestExportRoundTrip(t *testing.T) {
	s, p := seeded(t)
	f := must[*ImportFile](t)(s.Export(p.ID))
	if f.Project != "va" || len(f.Cases) != 3 || len(f.Areas) != 2 {
		t.Fatalf("%+v", f)
	}
	r := must[*ImportResult](t)(s.Import(*f, false, ActorClaude))
	if r.Unchanged != 3 {
		t.Fatalf("round trip changed cases: %+v", r)
	}
}

func TestResolveCase(t *testing.T) {
	s, p := seeded(t)
	c := caseByKey(t, s, p, "ui.idle")
	byID := must[*Case](t)(s.ResolveCase(p.ID, "#"+strconv.FormatInt(c.ID, 10)))
	if byID.Key != "ui.idle" {
		t.Fatalf("by id: %s", byID.Key)
	}
	if _, err := s.ResolveCase(p.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestTransitions(t *testing.T) {
	type step struct {
		op      string // pass fail blocked skip claim fixed
		want    string // resulting status, or "" for error
		errKind error
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"pass from untested", []step{{"pass", StatusPass, nil}}},
		{"skip keeps status", []step{{"skip", StatusUntested, nil}}},
		{"blocked", []step{{"blocked", StatusBlocked, nil}}},
		{"claim needs open bug", []step{{"claim", "", ErrInvalidTransition}}},
		{"fixed needs open bug", []step{{"pass", StatusPass, nil}, {"fixed", "", ErrInvalidTransition}}},
		{"full loop", []step{{"fail", StatusFail, nil}, {"claim", StatusInFix, nil}, {"fixed", StatusFixed, nil}, {"pass", StatusPass, nil}}},
		{"fixed straight from fail", []step{{"fail", StatusFail, nil}, {"fixed", StatusFixed, nil}}},
		{"claim blocked", []step{{"blocked", StatusBlocked, nil}, {"claim", StatusInFix, nil}}},
		{"cannot claim twice", []step{{"fail", StatusFail, nil}, {"claim", StatusInFix, nil}, {"claim", "", ErrInvalidTransition}}},
		{"cannot claim fixed", []step{{"fail", StatusFail, nil}, {"fixed", StatusFixed, nil}, {"claim", "", ErrInvalidTransition}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p := seeded(t)
			c := caseByKey(t, s, p, "asr.basic")
			for i, st := range tc.steps {
				var got *Case
				var err error
				switch st.op {
				case "pass", "blocked", "skip":
					got, err = s.RecordResult(c.ID, ResultInput{Result: st.op}, ActorUser)
				case "fail":
					got, err = s.RecordResult(c.ID, ResultInput{Result: st.op, Remarks: "r"}, ActorUser)
				case "claim":
					got, err = s.Claim(c.ID, ActorClaude)
				case "fixed":
					got, err = s.MarkFixed(c.ID, FixInput{Note: "n"}, ActorClaude)
				}
				if st.errKind != nil {
					if !errors.Is(err, st.errKind) {
						t.Fatalf("step %d %s: err %v, want %v", i, st.op, err, st.errKind)
					}
					continue
				}
				if err != nil || got.Status != st.want {
					t.Fatalf("step %d %s: status %v err %v, want %s", i, st.op, got, err, st.want)
				}
			}
		})
	}
}

func TestInvalidTransitionNamesStatus(t *testing.T) {
	s, p := seeded(t)
	c := caseByKey(t, s, p, "asr.basic")
	_, err := s.Claim(c.ID, ActorClaude)
	if err == nil || !strings.Contains(err.Error(), `"untested"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestReopenCounting(t *testing.T) {
	s, p := seeded(t)
	c := caseByKey(t, s, p, "asr.basic")
	fail(t, s, c)
	must[*Case](t)(s.MarkFixed(c.ID, FixInput{Note: "fixed the thing", Commit: "abc123", Files: []string{"a.go"}}, ActorClaude))
	got := fail(t, s, c)
	if got.ReopenCount != 1 || got.Status != StatusFail {
		t.Fatalf("%+v", got)
	}
	d := must[*CaseDetail](t)(s.CaseDetail(c.ID))
	if countKind(d.Events, "reopened") != 1 {
		t.Fatal("no reopened event")
	}
	if d.LastFix == nil || d.LastFix.Commit != "abc123" || d.LastFix.Files[0] != "a.go" {
		t.Fatalf("last fix %+v", d.LastFix)
	}
	// Failing again without an intervening fix does not count.
	got = fail(t, s, c)
	if got.ReopenCount != 1 {
		t.Fatalf("reopen_count = %d", got.ReopenCount)
	}
}

// Review focus 4.
func TestFailRemarks(t *testing.T) {
	s, p := seeded(t)
	c := caseByKey(t, s, p, "asr.basic")
	for _, r := range []string{"", "   \n\t"} {
		if _, err := s.RecordResult(c.ID, ResultInput{Result: ResultFail, Remarks: r}, ActorUser); !errors.Is(err, ErrValidation) {
			t.Fatalf("remarks %q: err %v", r, err)
		}
	}
	if c2 := caseByKey(t, s, p, "asr.basic"); c2.Status != StatusUntested {
		t.Fatalf("status changed on rejected input: %s", c2.Status)
	}
	long := strings.Repeat("ворота 🎤 — ", 400)
	got := must[*Case](t)(s.RecordResult(c.ID, ResultInput{Result: ResultFail, Remarks: long}, ActorUser))
	if got.Severity != "major" {
		t.Fatalf("default severity = %q", got.Severity)
	}
	bugs := must[[]Bug](t)(s.Bugs(p.ID, nil, ""))
	if len(bugs) != 1 || bugs[0].Remarks != strings.TrimSpace(long) {
		t.Fatalf("remarks did not round-trip")
	}
	if _, err := s.RecordResult(c.ID, ResultInput{Result: ResultFail, Remarks: "x", Severity: "huge"}, ActorUser); !errors.Is(err, ErrValidation) {
		t.Fatalf("bad severity: %v", err)
	}
	got = must[*Case](t)(s.RecordResult(c.ID, ResultInput{Result: ResultPass}, ActorUser))
	if got.Severity != "" {
		t.Fatalf("pass kept severity %q", got.Severity)
	}
	if _, err := s.MarkFixed(c.ID, FixInput{Note: " "}, ActorClaude); !errors.Is(err, ErrValidation) {
		t.Fatalf("fix without note: %v", err)
	}
}

func TestRunFilters(t *testing.T) {
	s, p := seeded(t)
	basic := caseByKey(t, s, p, "asr.basic")
	noise := caseByKey(t, s, p, "asr.noise")
	idle := caseByKey(t, s, p, "ui.idle")
	must[*Case](t)(s.RecordResult(basic.ID, ResultInput{Result: ResultPass}, ActorUser))
	fail(t, s, noise)
	// idle untested
	cases := []struct {
		filter string
		want   int
	}{{"needs-retest", 1}, {"open", 1}, {"all", 3}, {"area:asr", 2}, {"priority:P0", 1}, {"", 1}}
	for _, tc := range cases {
		r, err := s.CreateRun(p.ID, "", "b1", tc.filter, ActorUser)
		if err != nil || r.Progress.Total != tc.want {
			t.Fatalf("filter %q: run %+v err %v, want %d", tc.filter, r, err, tc.want)
		}
	}
	if _, err := s.CreateRun(p.ID, "", "", "bogus", ActorUser); !errors.Is(err, ErrValidation) {
		t.Fatalf("bogus filter: %v", err)
	}
	// Everything resolved → default picks all.
	must[*Case](t)(s.RecordResult(noise.ID, ResultInput{Result: ResultPass}, ActorUser))
	must[*Case](t)(s.RecordResult(idle.ID, ResultInput{Result: ResultPass}, ActorUser))
	r := must[*Run](t)(s.CreateRun(p.ID, "", "", "", ActorUser))
	if r.Filter != "all" || r.Progress.Total != 3 {
		t.Fatalf("default filter %+v", r)
	}
}

func TestRunResults(t *testing.T) {
	s, p := seeded(t)
	r := must[*Run](t)(s.CreateRun(p.ID, "", "abc", "all", ActorUser))
	c := caseByKey(t, s, p, "asr.basic")
	must[*Case](t)(s.RecordResult(c.ID, ResultInput{RunID: &r.ID, Result: ResultFail, Remarks: "first"}, ActorUser))
	must[*Case](t)(s.RecordResult(c.ID, ResultInput{RunID: &r.ID, Result: ResultPass}, ActorUser))
	d := must[*RunDetail](t)(s.GetRun(r.ID))
	if d.Progress.Done != 1 || d.Progress.Pass != 1 || d.Progress.Fail != 0 {
		t.Fatalf("progress %+v", d.Progress)
	}
	cd := must[*CaseDetail](t)(s.CaseDetail(c.ID))
	if countKind(cd.Events, "result") != 2 {
		t.Fatal("re-record lost history")
	}
	if cd.Events[1].Data["build"] != "abc" {
		t.Fatalf("result event lacks build: %+v", cd.Events[1].Data)
	}
}

// Review focus 3.
func TestResultRunGuards(t *testing.T) {
	s, p := seeded(t)
	basic := caseByKey(t, s, p, "asr.basic")
	r := must[*Run](t)(s.CreateRun(p.ID, "", "", "area:ui", ActorUser))
	if _, err := s.RecordResult(basic.ID, ResultInput{RunID: &r.ID, Result: ResultPass}, ActorUser); !errors.Is(err, ErrValidation) {
		t.Fatalf("case not in run: %v", err)
	}
	if err := s.CloseRun(r.ID, ActorUser); err != nil {
		t.Fatal(err)
	}
	idle := caseByKey(t, s, p, "ui.idle")
	if _, err := s.RecordResult(idle.ID, ResultInput{RunID: &r.ID, Result: ResultPass}, ActorUser); !errors.Is(err, ErrValidation) {
		t.Fatalf("closed run: %v", err)
	}
	if got := caseByKey(t, s, p, "ui.idle"); got.Status != StatusUntested {
		t.Fatalf("status changed: %s", got.Status)
	}
	if got := caseByKey(t, s, p, "asr.basic"); got.Status != StatusUntested {
		t.Fatalf("status changed: %s", got.Status)
	}
	if err := s.CloseRun(r.ID, ActorUser); !errors.Is(err, ErrValidation) {
		t.Fatalf("double close: %v", err)
	}
}

func TestAttachments(t *testing.T) {
	s, p := seeded(t)
	c := caseByKey(t, s, p, "asr.basic")
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 100)...)
	a1 := must[*Attachment](t)(s.AddAttachment(c.ID, nil, "shot.png", bytes.NewReader(png), ActorUser))
	a2 := must[*Attachment](t)(s.AddAttachment(c.ID, nil, "again.png", bytes.NewReader(png), ActorUser))
	if a1.Path != a2.Path || !strings.HasSuffix(a1.Path, a1.SHA256+".png") || a1.Mime != "image/png" {
		t.Fatalf("a1 %+v a2 %+v", a1, a2)
	}
	if b, err := os.ReadFile(a1.Path); err != nil || !bytes.Equal(b, png) {
		t.Fatalf("stored bytes differ: %v", err)
	}
	logAtt := must[*Attachment](t)(s.AddAttachment(c.ID, nil, "voice-agent.log", strings.NewReader("ERROR boom\n"), ActorUser))
	if !strings.HasPrefix(logAtt.Mime, "text/plain") {
		t.Fatalf("log mime %q", logAtt.Mime)
	}
	big := bytes.NewReader(make([]byte, MaxAttachment+1))
	if _, err := s.AddAttachment(c.ID, nil, "big.bin", big, ActorUser); !errors.Is(err, ErrValidation) {
		t.Fatalf("oversize: %v", err)
	}
	d := must[*CaseDetail](t)(s.CaseDetail(c.ID))
	if len(d.Attachments) != 3 || countKind(d.Events, "attachment") != 3 {
		t.Fatalf("attachments %d events %d", len(d.Attachments), countKind(d.Events, "attachment"))
	}
	got := must[*Attachment](t)(s.GetAttachment(a1.ID))
	if got.Filename != "shot.png" {
		t.Fatalf("%+v", got)
	}
}

func TestSummaryAndBugs(t *testing.T) {
	s, p := seeded(t)
	basic := caseByKey(t, s, p, "asr.basic")
	noise := caseByKey(t, s, p, "asr.noise")
	must[*Case](t)(s.RecordResult(basic.ID, ResultInput{Result: ResultFail, Remarks: "minor glitch", Severity: "minor"}, ActorUser))
	must[*Case](t)(s.RecordResult(noise.ID, ResultInput{Result: ResultFail, Remarks: "crash!", Severity: "critical"}, ActorUser))
	must[*Case](t)(s.MarkFixed(noise.ID, FixInput{Note: "guarded nil"}, ActorClaude))
	must[*Case](t)(s.RecordResult(noise.ID, ResultInput{Result: ResultFail, Remarks: "still crashes", Severity: "critical"}, ActorUser))
	must[*Attachment](t)(s.AddAttachment(noise.ID, nil, "x.txt", strings.NewReader("trace"), ActorUser))
	if err := s.Comment(noise.ID, "only with fan on", ActorUser); err != nil {
		t.Fatal(err)
	}

	sum := must[*Summary](t)(s.Summary(p.ID))
	if sum.Total != 3 || sum.ByStatus[StatusFail] != 2 || sum.OpenBugs != 2 || sum.NeedsRetest != 1 ||
		sum.BySeverity["critical"] != 1 || len(sum.Regressions) != 1 || len(sum.Areas) != 2 {
		t.Fatalf("summary %+v", sum)
	}
	bugs := must[[]Bug](t)(s.Bugs(p.ID, nil, ""))
	if len(bugs) != 2 || bugs[0].Key != "asr.noise" {
		t.Fatalf("bugs order %+v", bugs)
	}
	b := bugs[0]
	if b.Remarks != "still crashes" || b.ReopenCount != 1 || b.LastFix == nil || b.LastFix.Note != "guarded nil" ||
		len(b.Attachments) != 1 || !filepath.IsAbs(b.Attachments[0]) || len(b.Comments) != 1 {
		t.Fatalf("bug %+v", b)
	}
	if got := must[[]Bug](t)(s.Bugs(p.ID, nil, "ui")); len(got) != 0 {
		t.Fatalf("area filter: %d", len(got))
	}
}
