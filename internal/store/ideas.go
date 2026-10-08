package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Idea statuses.
const (
	IdeaNew        = "new"
	IdeaInProgress = "in_progress"
	IdeaDone       = "done" // built by Claude, waiting for the user to check
	IdeaAccepted   = "accepted"
	IdeaDeclined   = "declined"
)

var IdeaStatuses = []string{IdeaNew, IdeaInProgress, IdeaDone, IdeaAccepted, IdeaDeclined}

type Idea struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	Status      string `json:"status"`
	ReopenCount int    `json:"reopen_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type IdeaAttachment struct {
	ID        int64  `json:"id"`
	IdeaID    int64  `json:"idea_id"`
	Filename  string `json:"filename"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
}

type IdeaDetail struct {
	Idea
	Events      []Event          `json:"events"`
	Attachments []IdeaAttachment `json:"attachments"`
}

// IdeaWork is what Claude needs to act on an idea.
type IdeaWork struct {
	Idea
	Remarks     string   `json:"remarks,omitempty"` // why the user sent it back
	Comments    []string `json:"comments,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	LastNote    string   `json:"last_note,omitempty"` // Claude's previous done/decline/plan note
}

// IdeaUpdate is the payload for Claude's pick/done/decline.
type IdeaUpdate struct {
	Note   string   `json:"note"`
	Commit string   `json:"commit,omitempty"`
	Files  []string `json:"files,omitempty"`
	Cases  []string `json:"cases,omitempty"` // test-case keys added for the idea
}

// ideaTitle is the idea's first line, trimmed to a readable length.
func ideaTitle(text string) string {
	t := strings.TrimSpace(text)
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	if utf8.RuneCountInString(t) > 90 {
		t = string([]rune(t)[:87]) + "…"
	}
	return t
}

const ideaCols = `id, project_id, text, status, reopen_count, created_at, updated_at`

func scanIdea(r scanner) (*Idea, error) {
	var i Idea
	if err := r.Scan(&i.ID, &i.ProjectID, &i.Text, &i.Status, &i.ReopenCount, &i.CreatedAt, &i.UpdatedAt); err != nil {
		return nil, err
	}
	i.Title = ideaTitle(i.Text)
	return &i, nil
}

func getIdea(q querier, id int64) (*Idea, error) {
	i, err := scanIdea(q.QueryRow(`SELECT `+ideaCols+` FROM idea WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: idea %d", ErrNotFound, id)
	}
	return i, err
}

func (s *Store) GetIdea(id int64) (*Idea, error) { return getIdea(s.db, id) }

// ResolveIdea accepts "12" or "#12" and checks the project.
func (s *Store) ResolveIdea(projectID int64, ref string) (*Idea, error) {
	var id int64
	if _, err := fmt.Sscan(strings.TrimPrefix(strings.TrimSpace(ref), "#"), &id); err != nil {
		return nil, fmt.Errorf("%w: idea %q (use its number)", ErrNotFound, ref)
	}
	i, err := s.GetIdea(id)
	if err == nil && i.ProjectID != projectID {
		return nil, fmt.Errorf("%w: idea %d is in another project", ErrNotFound, id)
	}
	return i, err
}

func (s *Store) CreateIdea(projectID int64, text string, actor Actor) (*Idea, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("%w: idea is empty", ErrValidation)
	}
	var out *Idea
	err := s.tx(func(tx *sql.Tx) error {
		t := now()
		res, err := tx.Exec(`INSERT INTO idea(project_id, text, status, created_at, updated_at) VALUES (?,?,?,?,?)`,
			projectID, text, IdeaNew, t, t)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if _, err := addEvent(tx, eventSpec{projectID: projectID, ideaID: &id, actor: actor, kind: "idea_created", to: IdeaNew}); err != nil {
			return err
		}
		out, err = getIdea(tx, id)
		return err
	})
	return out, err
}

// EditIdea rewrites the idea text; only while nobody has started on it.
func (s *Store) EditIdea(id int64, text string, actor Actor) (*Idea, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("%w: idea is empty", ErrValidation)
	}
	var out *Idea
	err := s.tx(func(tx *sql.Tx) error {
		i, err := getIdea(tx, id)
		if err != nil {
			return err
		}
		if i.Status != IdeaNew {
			return fmt.Errorf("%w: idea #%d is %q; add a comment instead of editing", ErrInvalidTransition, id, i.Status)
		}
		if i.Text == text {
			out = i
			return nil
		}
		if _, err := tx.Exec(`UPDATE idea SET text = ?, updated_at = ? WHERE id = ?`, text, now(), id); err != nil {
			return err
		}
		if _, err := addEvent(tx, eventSpec{projectID: i.ProjectID, ideaID: &id, actor: actor, kind: "idea_edited",
			data: map[string]any{"previous": i.Text}}); err != nil {
			return err
		}
		out, err = getIdea(tx, id)
		return err
	})
	return out, err
}

func (s *Store) ListIdeas(projectID int64, statuses []string) ([]Idea, error) {
	q := `SELECT ` + ideaCols + ` FROM idea WHERE project_id = ?`
	args := []any{projectID}
	if len(statuses) > 0 {
		q += ` AND status IN (` + placeholders(len(statuses)) + `)`
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	// Ideas waiting on someone come first, newest activity on top.
	q += ` ORDER BY CASE status WHEN 'done' THEN 0 WHEN 'new' THEN 1 WHEN 'in_progress' THEN 2 ELSE 3 END, updated_at DESC, id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Idea{}
	for rows.Next() {
		i, err := scanIdea(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

func (s *Store) ideaEvents(id int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT `+eventCols+eventFrom+`WHERE e.idea_id = ? ORDER BY e.id`, id)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

func (s *Store) ideaAttachments(id int64) ([]IdeaAttachment, error) {
	rows, err := s.db.Query(`SELECT id, idea_id, filename, mime, size, path, created_at FROM idea_attachment WHERE idea_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IdeaAttachment{}
	for rows.Next() {
		var a IdeaAttachment
		if err := rows.Scan(&a.ID, &a.IdeaID, &a.Filename, &a.Mime, &a.Size, &a.Path, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) IdeaDetail(id int64) (*IdeaDetail, error) {
	i, err := s.GetIdea(id)
	if err != nil {
		return nil, err
	}
	d := &IdeaDetail{Idea: *i}
	if d.Events, err = s.ideaEvents(id); err != nil {
		return nil, err
	}
	if d.Attachments, err = s.ideaAttachments(id); err != nil {
		return nil, err
	}
	return d, nil
}

// ideaMove applies a status transition with an event.
func (s *Store) ideaMove(id int64, from []string, to, kind string, data map[string]any, reopen bool, actor Actor) (*Idea, error) {
	var out *Idea
	err := s.tx(func(tx *sql.Tx) error {
		i, err := getIdea(tx, id)
		if err != nil {
			return err
		}
		if !valid(from, i.Status) {
			return fmt.Errorf("%w: cannot %s idea #%d while it is %q (allowed from: %s)",
				ErrInvalidTransition, strings.TrimPrefix(kind, "idea_"), id, i.Status, strings.Join(from, ", "))
		}
		reopens := i.ReopenCount
		if reopen {
			reopens++
		}
		if _, err := tx.Exec(`UPDATE idea SET status = ?, reopen_count = ?, updated_at = ? WHERE id = ?`, to, reopens, now(), id); err != nil {
			return err
		}
		if _, err := addEvent(tx, eventSpec{projectID: i.ProjectID, ideaID: &id, actor: actor, kind: kind,
			from: i.Status, to: to, data: data}); err != nil {
			return err
		}
		out, err = getIdea(tx, id)
		return err
	})
	return out, err
}

func (u IdeaUpdate) data() map[string]any {
	d := map[string]any{}
	if u.Note = strings.TrimSpace(u.Note); u.Note != "" {
		d["note"] = u.Note
	}
	if u.Commit != "" {
		d["commit"] = u.Commit
	}
	if len(u.Files) > 0 {
		d["files"] = u.Files
	}
	if len(u.Cases) > 0 {
		d["cases"] = u.Cases
	}
	return d
}

// PickIdea: Claude starts on an idea (note = plan, optional).
func (s *Store) PickIdea(id int64, u IdeaUpdate, actor Actor) (*Idea, error) {
	return s.ideaMove(id, []string{IdeaNew}, IdeaInProgress, "idea_picked", u.data(), false, actor)
}

// FinishIdea: Claude built it; the user must check it.
func (s *Store) FinishIdea(id int64, u IdeaUpdate, actor Actor) (*Idea, error) {
	if strings.TrimSpace(u.Note) == "" {
		return nil, fmt.Errorf("%w: say what was built and how to try it (--note)", ErrValidation)
	}
	return s.ideaMove(id, []string{IdeaNew, IdeaInProgress}, IdeaDone, "idea_done", u.data(), false, actor)
}

// DeclineIdea: Claude won't build it, with a reason.
func (s *Store) DeclineIdea(id int64, u IdeaUpdate, actor Actor) (*Idea, error) {
	if strings.TrimSpace(u.Note) == "" {
		return nil, fmt.Errorf("%w: a decline needs a reason (--note)", ErrValidation)
	}
	return s.ideaMove(id, []string{IdeaNew, IdeaInProgress}, IdeaDeclined, "idea_declined", u.data(), false, actor)
}

// AcceptIdea: the user confirms the built idea works.
func (s *Store) AcceptIdea(id int64, actor Actor) (*Idea, error) {
	return s.ideaMove(id, []string{IdeaDone}, IdeaAccepted, "idea_accepted", nil, false, actor)
}

// ReopenIdea: the user sends a done/declined idea back with remarks.
func (s *Store) ReopenIdea(id int64, remarks string, actor Actor) (*Idea, error) {
	remarks = strings.TrimSpace(remarks)
	if remarks == "" {
		return nil, fmt.Errorf("%w: say what's not right so Claude can fix it", ErrValidation)
	}
	return s.ideaMove(id, []string{IdeaDone, IdeaDeclined, IdeaAccepted}, IdeaNew, "idea_reopened",
		map[string]any{"remarks": remarks}, true, actor)
}

func (s *Store) CommentIdea(id int64, text string, actor Actor) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("%w: comment is empty", ErrValidation)
	}
	return s.tx(func(tx *sql.Tx) error {
		i, err := getIdea(tx, id)
		if err != nil {
			return err
		}
		_, err = addEvent(tx, eventSpec{projectID: i.ProjectID, ideaID: &id, actor: actor, kind: "idea_comment",
			data: map[string]any{"text": text}})
		return err
	})
}

func (s *Store) AddIdeaAttachment(id int64, filename string, r io.Reader, actor Actor) (*IdeaAttachment, error) {
	b, err := s.saveBlob(filename, r)
	if err != nil {
		return nil, err
	}
	var a *IdeaAttachment
	err = s.tx(func(tx *sql.Tx) error {
		i, err := getIdea(tx, id)
		if err != nil {
			return err
		}
		t := now()
		res, err := tx.Exec(`INSERT INTO idea_attachment(idea_id, filename, mime, size, sha256, path, created_at) VALUES (?,?,?,?,?,?,?)`,
			id, b.filename, b.mime, len(b.data), b.sha, b.path, t)
		if err != nil {
			return err
		}
		aid, _ := res.LastInsertId()
		a = &IdeaAttachment{ID: aid, IdeaID: id, Filename: b.filename, Mime: b.mime, Size: int64(len(b.data)), Path: b.path, CreatedAt: t}
		_, err = addEvent(tx, eventSpec{projectID: i.ProjectID, ideaID: &id, actor: actor, kind: "idea_attachment",
			data: map[string]any{"filename": b.filename, "idea_attachment_id": aid}})
		return err
	})
	return a, err
}

func (s *Store) GetIdeaAttachment(id int64) (*IdeaAttachment, error) {
	var a IdeaAttachment
	err := s.db.QueryRow(`SELECT id, idea_id, filename, mime, size, path, created_at FROM idea_attachment WHERE id = ?`, id).
		Scan(&a.ID, &a.IdeaID, &a.Filename, &a.Mime, &a.Size, &a.Path, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: attachment %d", ErrNotFound, id)
	}
	return &a, err
}

// IdeasToWork lists ideas for Claude (default: new + in_progress) with the
// user's latest send-back remarks, comments since, and attachment paths.
func (s *Store) IdeasToWork(projectID int64, statuses []string) ([]IdeaWork, error) {
	if len(statuses) == 0 {
		statuses = []string{IdeaNew, IdeaInProgress}
	}
	ideas, err := s.ListIdeas(projectID, statuses)
	if err != nil {
		return nil, err
	}
	out := []IdeaWork{}
	for _, i := range ideas {
		w := IdeaWork{Idea: i}
		evs, err := s.ideaEvents(i.ID)
		if err != nil {
			return nil, err
		}
		var since int64
		for j := len(evs) - 1; j >= 0; j-- {
			if evs[j].Kind == "idea_reopened" {
				w.Remarks, _ = evs[j].Data["remarks"].(string)
				since = evs[j].ID
				break
			}
		}
		for _, e := range evs {
			switch e.Kind {
			case "idea_comment":
				if e.ID > since {
					if t, ok := e.Data["text"].(string); ok {
						w.Comments = append(w.Comments, "["+string(e.Actor)+"] "+t)
					}
				}
			case "idea_picked", "idea_done", "idea_declined":
				if n, ok := e.Data["note"].(string); ok {
					w.LastNote = n
				}
			}
		}
		atts, err := s.ideaAttachments(i.ID)
		if err != nil {
			return nil, err
		}
		for _, a := range atts {
			w.Attachments = append(w.Attachments, a.Path)
		}
		out = append(out, w)
	}
	return out, nil
}

func (s *Store) ideaCounts(projectID int64) (map[string]int, error) {
	out := map[string]int{}
	for _, st := range IdeaStatuses {
		out[st] = 0
	}
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM idea WHERE project_id = ? GROUP BY status`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}
