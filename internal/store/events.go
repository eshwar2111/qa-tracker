package store

import (
	"database/sql"
	"encoding/json"
)

type eventSpec struct {
	projectID  int64
	caseID     *int64
	runID      *int64
	ideaID     *int64
	actor      Actor
	kind       string
	from, to   string
	data       map[string]any
}

func addEvent(q querier, e eventSpec) (int64, error) {
	if e.actor != ActorUser && e.actor != ActorClaude {
		e.actor = ActorUser
	}
	if e.data == nil {
		e.data = map[string]any{}
	}
	b, err := json.Marshal(e.data)
	if err != nil {
		return 0, err
	}
	res, err := q.Exec(`INSERT INTO event(project_id, case_id, run_id, idea_id, actor, kind, from_status, to_status, data_json, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, e.projectID, e.caseID, e.runID, e.ideaID, string(e.actor), e.kind, e.from, e.to, string(b), now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const eventCols = `e.id, e.project_id, e.case_id, e.run_id, e.idea_id, e.actor, e.kind, e.from_status, e.to_status, e.data_json, e.created_at,
	COALESCE(c.key,''), COALESCE(c.title,''), COALESCE(i.text,'')`

const eventFrom = ` FROM event e LEFT JOIN test_case c ON c.id = e.case_id LEFT JOIN idea i ON i.id = e.idea_id `

func scanEvents(rows *sql.Rows) ([]Event, error) {
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var caseID, runID, ideaID sql.NullInt64
		var data, ideaText string
		if err := rows.Scan(&e.ID, &e.ProjectID, &caseID, &runID, &ideaID, &e.Actor, &e.Kind, &e.FromStatus, &e.ToStatus, &data, &e.CreatedAt, &e.CaseKey, &e.CaseTitle, &ideaText); err != nil {
			return nil, err
		}
		if ideaID.Valid {
			e.IdeaID = &ideaID.Int64
			e.IdeaTitle = ideaTitle(ideaText)
		}
		if caseID.Valid {
			e.CaseID = &caseID.Int64
		}
		if runID.Valid {
			e.RunID = &runID.Int64
		}
		_ = json.Unmarshal([]byte(data), &e.Data)
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventsSince returns up to limit events with id > since, oldest first. It is
// the UI's live-update cursor: callers page by passing the last id back.
func (s *Store) EventsSince(projectID, since int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT `+eventCols+eventFrom+`WHERE e.project_id = ? AND e.id > ? ORDER BY e.id LIMIT ?`, projectID, since, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// LatestEventID returns the current max event id for a project (0 if none).
func (s *Store) LatestEventID(projectID int64) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(id) FROM event WHERE project_id = ?`, projectID).Scan(&id)
	return id.Int64, err
}

func (s *Store) caseEvents(caseID int64) ([]Event, error) {
	rows, err := s.db.Query(`SELECT `+eventCols+eventFrom+`WHERE e.case_id = ? ORDER BY e.id`, caseID)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

func (s *Store) recentEvents(projectID int64, n int) ([]Event, error) {
	rows, err := s.db.Query(`SELECT `+eventCols+eventFrom+`WHERE e.project_id = ? ORDER BY e.id DESC LIMIT ?`, projectID, n)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}
