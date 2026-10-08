package server

import (
	"errors"
	"net/http"

	"qa-tracker/internal/store"
)

func (s *Server) ideas(w http.ResponseWriter, r *http.Request, p *store.Project) {
	is, err := s.st.ListIdeas(p.ID, splitList(r.URL.Query().Get("status")))
	reply(w, is, err)
}

func (s *Server) createIdea(w http.ResponseWriter, r *http.Request, p *store.Project) {
	var in struct{ Text string }
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	i, err := s.st.CreateIdea(p.ID, in.Text, store.ActorUser)
	reply(w, i, err)
}

func (s *Server) ideaDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.st.IdeaDetail(id)
	reply(w, d, err)
}

func (s *Server) editIdea(w http.ResponseWriter, r *http.Request) {
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
	i, err := s.st.EditIdea(id, in.Text, store.ActorUser)
	reply(w, i, err)
}

func (s *Server) acceptIdea(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	i, err := s.st.AcceptIdea(id, store.ActorUser)
	reply(w, i, err)
}

func (s *Server) reopenIdea(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct{ Remarks string }
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	i, err := s.st.ReopenIdea(id, in.Remarks, store.ActorUser)
	reply(w, i, err)
}

func (s *Server) commentIdea(w http.ResponseWriter, r *http.Request) {
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
	reply(w, map[string]bool{"ok": true}, s.st.CommentIdea(id, in.Text, store.ActorUser))
}

func (s *Server) uploadIdea(w http.ResponseWriter, r *http.Request) {
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
	a, err := s.st.AddIdeaAttachment(id, hdr.Filename, file, store.ActorUser)
	reply(w, a, err)
}

func (s *Server) ideaAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	a, err := s.st.GetIdeaAttachment(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	serveFile(w, r, a.Path, a.Mime, a.Filename)
}
