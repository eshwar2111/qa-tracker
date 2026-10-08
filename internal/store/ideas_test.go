package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestIdeaLifecycle(t *testing.T) {
	s, p := seeded(t)
	i := must[*Idea](t)(s.CreateIdea(p.ID, "  Add a push-to-talk hotkey\nHold F9 to talk, release to send.  ", ActorUser))
	if i.Status != IdeaNew || i.Title != "Add a push-to-talk hotkey" || strings.HasPrefix(i.Text, " ") {
		t.Fatalf("created %+v", i)
	}
	if _, err := s.CreateIdea(p.ID, "  \n ", ActorUser); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty idea: %v", err)
	}
	// Editable only while new.
	must[*Idea](t)(s.EditIdea(i.ID, "Add a push-to-talk hotkey (F9)", ActorUser))

	must[*Idea](t)(s.PickIdea(i.ID, IdeaUpdate{Note: "Use RegisterHotKey"}, ActorClaude))
	if _, err := s.EditIdea(i.ID, "changed", ActorUser); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("edit in progress: %v", err)
	}
	if _, err := s.FinishIdea(i.ID, IdeaUpdate{}, ActorClaude); !errors.Is(err, ErrValidation) {
		t.Fatalf("done without note: %v", err)
	}
	if _, err := s.AcceptIdea(i.ID, ActorUser); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accept before done: %v", err)
	}
	must[*Idea](t)(s.FinishIdea(i.ID, IdeaUpdate{Note: "F9 push-to-talk", Commit: "abc", Cases: []string{"asr.basic"}}, ActorClaude))

	if _, err := s.ReopenIdea(i.ID, " ", ActorUser); !errors.Is(err, ErrValidation) {
		t.Fatalf("reopen without remarks: %v", err)
	}
	got := must[*Idea](t)(s.ReopenIdea(i.ID, "F9 clashes with my IDE, use F8", ActorUser))
	if got.Status != IdeaNew || got.ReopenCount != 1 {
		t.Fatalf("reopened %+v", got)
	}
	if err := s.CommentIdea(i.ID, "also show a mic icon", ActorUser); err != nil {
		t.Fatal(err)
	}
	must[*IdeaAttachment](t)(s.AddIdeaAttachment(i.ID, "mock.png", strings.NewReader("\x89PNG\r\n\x1a\nxxxx"), ActorUser))

	work := must[[]IdeaWork](t)(s.IdeasToWork(p.ID, nil))
	if len(work) != 1 {
		t.Fatalf("work %d", len(work))
	}
	w := work[0]
	if w.Remarks != "F9 clashes with my IDE, use F8" || w.LastNote != "F9 push-to-talk" ||
		len(w.Comments) != 1 || len(w.Attachments) != 1 || !filepath.IsAbs(w.Attachments[0]) {
		t.Fatalf("work item %+v", w)
	}

	must[*Idea](t)(s.FinishIdea(i.ID, IdeaUpdate{Note: "Now F8"}, ActorClaude))
	must[*Idea](t)(s.AcceptIdea(i.ID, ActorUser))
	if n := len(must[[]IdeaWork](t)(s.IdeasToWork(p.ID, nil))); n != 0 {
		t.Fatalf("accepted idea still to work: %d", n)
	}

	d := must[*IdeaDetail](t)(s.IdeaDetail(i.ID))
	var kinds []string
	for _, e := range d.Events {
		kinds = append(kinds, e.Kind)
	}
	want := "idea_created,idea_edited,idea_picked,idea_done,idea_reopened,idea_comment,idea_attachment,idea_done,idea_accepted"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("timeline %v", kinds)
	}
	if d.Events[0].IdeaTitle == "" || d.Events[0].IdeaID == nil {
		t.Fatalf("event lacks idea ref %+v", d.Events[0])
	}

	sum := must[*Summary](t)(s.Summary(p.ID))
	if sum.Ideas[IdeaAccepted] != 1 {
		t.Fatalf("summary ideas %+v", sum.Ideas)
	}
}

func TestDeclineAndReopen(t *testing.T) {
	s, p := seeded(t)
	i := must[*Idea](t)(s.CreateIdea(p.ID, "Rewrite in Rust", ActorUser))
	if _, err := s.DeclineIdea(i.ID, IdeaUpdate{}, ActorClaude); !errors.Is(err, ErrValidation) {
		t.Fatalf("decline without reason: %v", err)
	}
	must[*Idea](t)(s.DeclineIdea(i.ID, IdeaUpdate{Note: "Go toolchain is load-bearing"}, ActorClaude))
	got := must[*Idea](t)(s.ReopenIdea(i.ID, "ok, just the audio layer then", ActorUser))
	if got.Status != IdeaNew {
		t.Fatalf("%+v", got)
	}
	if _, err := s.ResolveIdea(p.ID, "#"+strconv.FormatInt(i.ID, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveIdea(p.ID, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad ref: %v", err)
	}
}

// A DB created before ideas existed gains the idea_id column on Open.
func TestMigratesOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE project (id INTEGER PRIMARY KEY, key TEXT NOT NULL UNIQUE, name TEXT NOT NULL, repo_path TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
		CREATE TABLE event (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL, case_id INTEGER, run_id INTEGER,
			actor TEXT NOT NULL, kind TEXT NOT NULL, from_status TEXT NOT NULL DEFAULT '', to_status TEXT NOT NULL DEFAULT '',
			data_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL);
		INSERT INTO project(key, name, created_at) VALUES ('va','va','x');
		INSERT INTO event(project_id, actor, kind, created_at) VALUES (1,'user','project_created','x');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := must[*Idea](t)(s.CreateIdea(1, "works after upgrade", ActorUser))
	evs := must[[]Event](t)(s.EventsSince(1, 0, 10))
	if len(evs) != 2 || evs[1].IdeaID == nil || *evs[1].IdeaID != i.ID {
		t.Fatalf("events after migration %+v", evs)
	}
}
