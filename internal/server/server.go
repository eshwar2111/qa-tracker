// Package server exposes the store over a JSON REST API and serves the
// embedded web UI. Handlers are thin: parse, call store, encode.
package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"qa-tracker/internal/store"
	"qa-tracker/web"
)

type Server struct {
	st  *store.Store
	mux *http.ServeMux
}

func New(st *store.Store) *Server {
	s := &Server{st: st, mux: http.NewServeMux()}
	m := s.mux
	m.HandleFunc("GET /api/projects", s.projects)
	m.HandleFunc("GET /api/projects/{p}/summary", s.withProject(s.summary))
	m.HandleFunc("GET /api/projects/{p}/areas", s.withProject(s.areas))
	m.HandleFunc("GET /api/projects/{p}/cases", s.withProject(s.cases))
	m.HandleFunc("GET /api/projects/{p}/runs", s.withProject(s.runs))
	m.HandleFunc("POST /api/projects/{p}/runs", s.withProject(s.createRun))
	m.HandleFunc("GET /api/cases/{id}", s.caseDetail)
	m.HandleFunc("POST /api/cases/{id}/result", s.result)
	m.HandleFunc("POST /api/cases/{id}/comment", s.comment)
	m.HandleFunc("POST /api/cases/{id}/attachments", s.upload)
	m.HandleFunc("GET /api/attachments/{id}", s.attachment)
	m.HandleFunc("GET /api/runs/{id}", s.run)
	m.HandleFunc("POST /api/runs/{id}/close", s.closeRun)
	m.HandleFunc("GET /api/events", s.events)

	static, _ := fs.Sub(web.Static, "static")
	m.Handle("GET /", http.FileServerFS(static))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, store.ErrValidation), errors.Is(err, store.ErrInvalidTransition):
		code = http.StatusBadRequest
	default:
		log.Printf("error: %v", err)
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, store.ErrNotFound
	}
	return id, nil
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v); err != nil {
		return errors.Join(store.ErrValidation, err)
	}
	return nil
}

type projectHandler func(w http.ResponseWriter, r *http.Request, p *store.Project)

func (s *Server) withProject(h projectHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.st.ResolveProject(r.PathValue("p"))
		if err != nil {
			writeErr(w, err)
			return
		}
		h(w, r, p)
	}
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.st.ListProjects()
	reply(w, ps, err)
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request, p *store.Project) {
	sum, err := s.st.Summary(p.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	latest, _ := s.st.LatestEventID(p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"project": p, "summary": sum, "latest_event": latest})
}

func (s *Server) areas(w http.ResponseWriter, r *http.Request, p *store.Project) {
	as, err := s.st.ListAreas(p.ID)
	reply(w, as, err)
}

func splitList(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) cases(w http.ResponseWriter, r *http.Request, p *store.Project) {
	q := r.URL.Query()
	statuses := splitList(q.Get("status"))
	// Convenience groups used by the UI's quick filters.
	switch q.Get("status") {
	case "open":
		statuses = store.OpenStatuses
	case "needs-retest":
		statuses = store.RetestStatuses
	}
	cs, err := s.st.ListCases(p.ID, store.CaseFilter{
		Statuses: statuses, AreaKey: q.Get("area"), Priority: q.Get("priority"), Q: q.Get("q"),
		IncludeArchived: q.Get("archived") == "1",
	})
	reply(w, cs, err)
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request, p *store.Project) {
	rs, err := s.st.ListRuns(p.ID)
	reply(w, rs, err)
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request, p *store.Project) {
	var in struct{ Name, Build, Filter string }
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	run, err := s.st.CreateRun(p.ID, in.Name, in.Build, in.Filter, store.ActorUser)
	reply(w, run, err)
}

func (s *Server) caseDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.st.CaseDetail(id)
	reply(w, d, err)
}

func (s *Server) result(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in store.ResultInput
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	c, err := s.st.RecordResult(id, in, store.ActorUser)
	reply(w, c, err)
}

func (s *Server) comment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct{ Text string }
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	reply(w, map[string]bool{"ok": true}, s.st.Comment(id, in.Text, store.ActorUser))
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxAttachment+1<<20)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, errors.Join(store.ErrValidation, err))
		return
	}
	defer file.Close()
	var runID *int64
	if v := r.FormValue("run_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			runID = &n
		}
	}
	a, err := s.st.AddAttachment(id, runID, hdr.Filename, file, store.ActorUser)
	reply(w, a, err)
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	a, err := s.st.GetAttachment(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	f, err := os.Open(a.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer f.Close()
	inline := strings.HasPrefix(a.Mime, "image/") || strings.HasPrefix(a.Mime, "text/") || a.Mime == "application/pdf"
	disp := "attachment"
	if inline {
		disp = "inline"
	}
	ct := a.Mime
	if strings.HasPrefix(ct, "text/") {
		ct = "text/plain; charset=utf-8" // never render uploaded HTML
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", disp+`; filename="`+strings.ReplaceAll(a.Filename, `"`, "")+`"`)
	http.ServeContent(w, r, "", fileModTime(f), f)
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.st.GetRun(id)
	reply(w, d, err)
}

func (s *Server) closeRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	reply(w, map[string]bool{"ok": true}, s.st.CloseRun(id, store.ActorUser))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.ResolveProject(r.URL.Query().Get("project"))
	if err != nil {
		writeErr(w, err)
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	evs, err := s.st.EventsSince(p.ID, since, 200)
	reply(w, evs, err)
}
