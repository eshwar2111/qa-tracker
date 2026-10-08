package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"qa-tracker/internal/store"
)

func setup(t *testing.T) (*httptest.Server, *store.Store, *store.Project) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "qa.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p, err := st.CreateProject("va", "Voice Agent", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Import(store.ImportFile{Project: "va",
		Areas: []store.ImportArea{{Key: "asr", Name: "ASR"}},
		Cases: []store.ImportCase{
			{Key: "asr.a", Area: "asr", Title: "A", Priority: "P0", Steps: []string{"s"}, Expected: "e"},
			{Key: "asr.b", Area: "asr", Title: "B", Priority: "P1", Steps: []string{"s"}, Expected: "e"},
		}}, false, store.ActorClaude)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(st))
	t.Cleanup(ts.Close)
	return ts, st, p
}

func call(t *testing.T, method, url string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func TestSummaryAndCases(t *testing.T) {
	ts, _, _ := setup(t)
	var sum struct {
		Summary     store.Summary `json:"summary"`
		LatestEvent int64         `json:"latest_event"`
	}
	if code := call(t, "GET", ts.URL+"/api/projects/va/summary", nil, &sum); code != 200 || sum.Summary.Total != 2 || sum.LatestEvent == 0 {
		t.Fatalf("summary %d %+v", code, sum)
	}
	var cases []store.Case
	call(t, "GET", ts.URL+"/api/projects/va/cases?priority=P0", nil, &cases)
	if len(cases) != 1 || cases[0].Key != "asr.a" {
		t.Fatalf("filtered cases %+v", cases)
	}
	call(t, "GET", ts.URL+"/api/projects/va/cases?status=needs-retest", nil, &cases)
	if len(cases) != 2 {
		t.Fatalf("needs-retest %d", len(cases))
	}
	if code := call(t, "GET", ts.URL+"/api/projects/nope/summary", nil, nil); code != 404 {
		t.Fatalf("unknown project %d", code)
	}
}

func TestResultAndRun(t *testing.T) {
	ts, st, p := setup(t)
	c, _ := st.ResolveCase(p.ID, "asr.a")
	var errBody map[string]string
	if code := call(t, "POST", fmt.Sprintf("%s/api/cases/%d/result", ts.URL, c.ID), map[string]any{"result": "fail"}, &errBody); code != 400 || !strings.Contains(errBody["error"], "remarks") {
		t.Fatalf("fail without remarks: %d %v", code, errBody)
	}
	var run store.Run
	if code := call(t, "POST", ts.URL+"/api/projects/va/runs", map[string]any{"build": "abc", "filter": "all"}, &run); code != 200 || run.Progress.Total != 2 {
		t.Fatalf("create run %d %+v", code, run)
	}
	var got store.Case
	call(t, "POST", fmt.Sprintf("%s/api/cases/%d/result", ts.URL, c.ID), map[string]any{"run_id": run.ID, "result": "fail", "remarks": "nope", "severity": "minor"}, &got)
	if got.Status != "fail" || got.Severity != "minor" {
		t.Fatalf("after fail %+v", got)
	}
	var rd store.RunDetail
	call(t, "GET", fmt.Sprintf("%s/api/runs/%d", ts.URL, run.ID), nil, &rd)
	if rd.Progress.Done != 1 || rd.Cases[0].Remarks != "nope" {
		t.Fatalf("run detail %+v", rd.Progress)
	}
	if code := call(t, "POST", fmt.Sprintf("%s/api/runs/%d/close", ts.URL, run.ID), nil, nil); code != 200 {
		t.Fatalf("close %d", code)
	}
	if code := call(t, "GET", ts.URL+"/api/cases/99999", nil, nil); code != 404 {
		t.Fatalf("missing case %d", code)
	}
	var evs []store.Event
	call(t, "GET", ts.URL+"/api/events?project=va&since=0", nil, &evs)
	if len(evs) < 5 || evs[len(evs)-1].Kind != "run_closed" {
		t.Fatalf("events %d", len(evs))
	}
}

func TestUploadAndServe(t *testing.T) {
	ts, st, p := setup(t)
	c, _ := st.ResolveCase(p.ID, "asr.a")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "trace.log")
	fw.Write([]byte("<script>alert(1)</script> ERROR"))
	mw.Close()
	res, err := http.Post(fmt.Sprintf("%s/api/cases/%d/attachments", ts.URL, c.ID), mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	var a store.Attachment
	json.NewDecoder(res.Body).Decode(&a)
	res.Body.Close()
	if res.StatusCode != 200 || a.ID == 0 {
		t.Fatalf("upload %d %+v", res.StatusCode, a)
	}
	res, _ = http.Get(fmt.Sprintf("%s/api/attachments/%d", ts.URL, a.ID))
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "<script>alert(1)</script> ERROR" || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("served %q as %s", b, res.Header.Get("Content-Type"))
	}
}

func TestServesIndex(t *testing.T) {
	ts, _, _ := setup(t)
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(b), "<title>") {
		t.Fatalf("index %d", res.StatusCode)
	}
}

func TestIdeasAPI(t *testing.T) {
	ts, st, _ := setup(t)
	var i store.Idea
	if code := call(t, "POST", ts.URL+"/api/projects/va/ideas", map[string]any{"text": "Dark mode toggle\nfor the island"}, &i); code != 200 || i.Title != "Dark mode toggle" {
		t.Fatalf("create %d %+v", code, i)
	}
	if code := call(t, "POST", ts.URL+"/api/projects/va/ideas", map[string]any{"text": " "}, nil); code != 400 {
		t.Fatalf("empty idea %d", code)
	}
	if code := call(t, "PUT", fmt.Sprintf("%s/api/ideas/%d", ts.URL, i.ID), map[string]any{"text": "Dark mode toggle v2"}, &i); code != 200 || i.Text != "Dark mode toggle v2" {
		t.Fatalf("edit %d %+v", code, i)
	}
	if code := call(t, "POST", fmt.Sprintf("%s/api/ideas/%d/accept", ts.URL, i.ID), nil, nil); code != 400 {
		t.Fatalf("accept new idea should be 400, got %d", code)
	}
	st.FinishIdea(i.ID, store.IdeaUpdate{Note: "built"}, store.ActorClaude)
	if code := call(t, "POST", fmt.Sprintf("%s/api/ideas/%d/reopen", ts.URL, i.ID), map[string]any{"remarks": "toggle is hidden"}, &i); code != 200 || i.Status != "new" {
		t.Fatalf("reopen %d %+v", code, i)
	}
	var list []store.Idea
	call(t, "GET", ts.URL+"/api/projects/va/ideas?status=new", nil, &list)
	if len(list) != 1 {
		t.Fatalf("list %d", len(list))
	}
	var d store.IdeaDetail
	call(t, "GET", fmt.Sprintf("%s/api/ideas/%d", ts.URL, i.ID), nil, &d)
	if len(d.Events) != 4 {
		t.Fatalf("events %d", len(d.Events))
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "sketch.txt")
	fw.Write([]byte("sketch"))
	mw.Close()
	res, err := http.Post(fmt.Sprintf("%s/api/ideas/%d/attachments", ts.URL, i.ID), mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	var a store.IdeaAttachment
	json.NewDecoder(res.Body).Decode(&a)
	res.Body.Close()
	res, _ = http.Get(fmt.Sprintf("%s/api/idea-attachments/%d", ts.URL, a.ID))
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "sketch" {
		t.Fatalf("served %q", b)
	}
}
