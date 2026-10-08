package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const caseCols = `c.id, c.project_id, c.area_id, a.key, a.name, c.key, c.title, c.preconditions, c.steps_json, c.expected,
	c.priority, c.tags, c.status, COALESCE(c.severity,''), c.version, c.reopen_count, c.archived, c.created_at, c.updated_at`

const caseFrom = ` FROM test_case c JOIN area a ON a.id = c.area_id `

type scanner interface{ Scan(...any) error }

func scanCase(r scanner, extra ...any) (*Case, error) {
	var c Case
	var steps, tags string
	var archived int
	dest := []any{&c.ID, &c.ProjectID, &c.AreaID, &c.AreaKey, &c.AreaName, &c.Key, &c.Title, &c.Preconditions, &steps, &c.Expected,
		&c.Priority, &tags, &c.Status, &c.Severity, &c.Version, &c.ReopenCount, &archived, &c.CreatedAt, &c.UpdatedAt}
	if err := r.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	c.Steps = []string{}
	_ = json.Unmarshal([]byte(steps), &c.Steps)
	c.Tags = splitTags(tags)
	c.Archived = archived != 0
	return &c, nil
}

func splitTags(s string) []string {
	out := []string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func getCase(q querier, id int64) (*Case, error) {
	c, err := scanCase(q.QueryRow(`SELECT `+caseCols+caseFrom+`WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: case %d", ErrNotFound, id)
	}
	return c, err
}

func (s *Store) GetCase(id int64) (*Case, error) { return getCase(s.db, id) }

// ResolveCase accepts a numeric id or a case key.
func (s *Store) ResolveCase(projectID int64, ref string) (*Case, error) {
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "#"))
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		c, err := s.GetCase(id)
		if err == nil && c.ProjectID != projectID {
			return nil, fmt.Errorf("%w: case %d is in another project", ErrNotFound, id)
		}
		return c, err
	}
	c, err := scanCase(s.db.QueryRow(`SELECT `+caseCols+caseFrom+`WHERE c.project_id = ? AND c.key = ?`, projectID, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: case %q", ErrNotFound, ref)
	}
	return c, err
}

func (s *Store) ListCases(projectID int64, f CaseFilter) ([]Case, error) {
	where := []string{"c.project_id = ?"}
	args := []any{projectID}
	if !f.IncludeArchived {
		where = append(where, "c.archived = 0")
	}
	if len(f.Statuses) > 0 {
		where = append(where, "c.status IN ("+placeholders(len(f.Statuses))+")")
		for _, st := range f.Statuses {
			args = append(args, st)
		}
	}
	if f.AreaKey != "" {
		where = append(where, "a.key = ?")
		args = append(args, f.AreaKey)
	}
	if f.Priority != "" {
		where = append(where, "c.priority = ?")
		args = append(args, f.Priority)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, "(c.title LIKE ? OR c.key LIKE ? OR c.steps_json LIKE ? OR c.expected LIKE ? OR c.tags LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like, like, like)
	}
	rows, err := s.db.Query(`SELECT `+caseCols+caseFrom+`WHERE `+strings.Join(where, " AND ")+
		` ORDER BY a.sort_order, a.key, c.priority, c.key`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Case{}
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (s *Store) CaseDetail(id int64) (*CaseDetail, error) {
	c, err := s.GetCase(id)
	if err != nil {
		return nil, err
	}
	d := &CaseDetail{Case: *c}
	if d.Events, err = s.caseEvents(id); err != nil {
		return nil, err
	}
	if d.Attachments, err = s.caseAttachments(id); err != nil {
		return nil, err
	}
	d.LastFix = lastFix(d.Events)
	rows, err := s.db.Query(`SELECT version, title, preconditions, steps_json, expected, priority, created_at
		FROM case_version WHERE case_id = ? ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.Versions = []CaseVersion{}
	for rows.Next() {
		var v CaseVersion
		var steps string
		if err := rows.Scan(&v.Version, &v.Title, &v.Preconditions, &steps, &v.Expected, &v.Priority, &v.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(steps), &v.Steps)
		d.Versions = append(d.Versions, v)
	}
	return d, rows.Err()
}

// lastFix extracts the newest "fixed" event from an ascending event list.
func lastFix(events []Event) *FixInfo {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "fixed" {
			return fixFromData(events[i].Data, events[i].CreatedAt)
		}
	}
	return nil
}

func fixFromData(d map[string]any, at string) *FixInfo {
	f := &FixInfo{CreatedAt: at}
	f.Note, _ = d["note"].(string)
	f.Commit, _ = d["commit"].(string)
	if files, ok := d["files"].([]any); ok {
		for _, x := range files {
			if s, ok := x.(string); ok {
				f.Files = append(f.Files, s)
			}
		}
	}
	return f
}
