package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// runFilter converts a spec §6 filter string into a CaseFilter.
func runFilter(filter string) (CaseFilter, error) {
	switch {
	case filter == "fixed":
		return CaseFilter{Statuses: []string{StatusFixed}}, nil
	case filter == "needs-retest":
		return CaseFilter{Statuses: RetestStatuses}, nil
	case filter == "open":
		return CaseFilter{Statuses: OpenStatuses}, nil
	case filter == "all":
		return CaseFilter{}, nil
	case strings.HasPrefix(filter, "area:"):
		return CaseFilter{AreaKey: strings.TrimPrefix(filter, "area:")}, nil
	case strings.HasPrefix(filter, "priority:"):
		p := strings.TrimPrefix(filter, "priority:")
		if !valid(Priorities, p) {
			return CaseFilter{}, fmt.Errorf("%w: priority %q", ErrValidation, p)
		}
		return CaseFilter{Priority: p}, nil
	}
	return CaseFilter{}, fmt.Errorf("%w: filter %q must be fixed, needs-retest, open, all, area:<key> or priority:<P0-P3>", ErrValidation, filter)
}

// CreateRun snapshots the cases matching filter into a new run. An empty
// filter picks the first non-empty of: fixed (Claude's fixes awaiting retest),
// needs-retest, all.
func (s *Store) CreateRun(projectID int64, name, build, filter string, actor Actor) (*Run, error) {
	if filter == "" {
		filter = "all"
		for _, f := range []struct {
			name     string
			statuses []string
		}{{"fixed", []string{StatusFixed}}, {"needs-retest", RetestStatuses}} {
			if n, _ := s.ListCases(projectID, CaseFilter{Statuses: f.statuses}); len(n) > 0 {
				filter = f.name
				break
			}
		}
	}
	cf, err := runFilter(filter)
	if err != nil {
		return nil, err
	}
	cases, err := s.ListCases(projectID, cf)
	if err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("%w: filter %q matches no cases", ErrValidation, filter)
	}
	if name == "" {
		name = "Run"
		if build != "" {
			name = "Build " + build
		}
	}
	var id int64
	err = s.tx(func(tx *sql.Tx) error {
		r, err := tx.Exec(`INSERT INTO run(project_id, name, build, filter_json, created_at) VALUES (?,?,?,?,?)`,
			projectID, name, build, filter, now())
		if err != nil {
			return err
		}
		id, _ = r.LastInsertId()
		for _, c := range cases {
			if _, err := tx.Exec(`INSERT INTO run_case(run_id, case_id, case_version) VALUES (?,?,?)`, id, c.ID, c.Version); err != nil {
				return err
			}
		}
		_, err = addEvent(tx, eventSpec{projectID: projectID, runID: &id, actor: actor, kind: "run_created",
			data: map[string]any{"name": name, "build": build, "filter": filter, "cases": len(cases)}})
		return err
	})
	if err != nil {
		return nil, err
	}
	d, err := s.GetRun(id)
	if err != nil {
		return nil, err
	}
	return &d.Run, nil
}

const runCols = `r.id, r.project_id, r.name, r.build, r.filter_json, r.created_at, COALESCE(r.closed_at,''),
	(SELECT COUNT(*) FROM run_case x WHERE x.run_id = r.id),
	(SELECT COUNT(*) FROM run_case x WHERE x.run_id = r.id AND x.result IS NOT NULL),
	(SELECT COUNT(*) FROM run_case x WHERE x.run_id = r.id AND x.result = 'pass'),
	(SELECT COUNT(*) FROM run_case x WHERE x.run_id = r.id AND x.result = 'fail'),
	(SELECT COUNT(*) FROM run_case x WHERE x.run_id = r.id AND x.result = 'blocked'),
	(SELECT COUNT(*) FROM run_case x WHERE x.run_id = r.id AND x.result = 'skip')`

func scanRun(r scanner) (*Run, error) {
	var x Run
	p := &x.Progress
	err := r.Scan(&x.ID, &x.ProjectID, &x.Name, &x.Build, &x.Filter, &x.CreatedAt, &x.ClosedAt,
		&p.Total, &p.Done, &p.Pass, &p.Fail, &p.Blocked, &p.Skip)
	return &x, err
}

func (s *Store) ListRuns(projectID int64) ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM run r WHERE r.project_id = ? ORDER BY r.id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) GetRun(id int64) (*RunDetail, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM run r WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: run %d", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	d := &RunDetail{Run: *r, Cases: []RunCase{}}
	rows, err := s.db.Query(`SELECT `+caseCols+`, rc.case_version, COALESCE(rc.result,''), rc.remarks, COALESCE(rc.severity,''), COALESCE(rc.executed_at,'')
		FROM run_case rc JOIN test_case c ON c.id = rc.case_id JOIN area a ON a.id = c.area_id
		WHERE rc.run_id = ? ORDER BY a.sort_order, a.key, c.priority, c.key`, id)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var rc RunCase
		c, err := scanCase(rows, &rc.CaseVersion, &rc.Result, &rc.Remarks, &rc.RunSeverity, &rc.ExecutedAt)
		if err != nil {
			rows.Close()
			return nil, err
		}
		rc.Case = *c
		d.Cases = append(d.Cases, rc)
		ids = append(ids, c.ID)
	}
	rows.Close()
	fixes, err := s.lastFixes(ids)
	if err != nil {
		return nil, err
	}
	for i := range d.Cases {
		d.Cases[i].LastFix = fixes[d.Cases[i].ID]
	}
	return d, nil
}

// lastFixes returns the newest fix per case id.
func (s *Store) lastFixes(ids []int64) (map[int64]*FixInfo, error) {
	out := map[int64]*FixInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT `+eventCols+eventFrom+`WHERE e.kind = 'fixed' AND e.case_id IN (`+placeholders(len(ids))+`) ORDER BY e.id`, args...)
	if err != nil {
		return nil, err
	}
	evs, err := scanEvents(rows)
	if err != nil {
		return nil, err
	}
	for _, e := range evs {
		out[*e.CaseID] = fixFromData(e.Data, e.CreatedAt)
	}
	return out, nil
}

func (s *Store) CloseRun(id int64, actor Actor) error {
	return s.tx(func(tx *sql.Tx) error {
		var projectID int64
		var closed sql.NullString
		err := tx.QueryRow(`SELECT project_id, closed_at FROM run WHERE id = ?`, id).Scan(&projectID, &closed)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: run %d", ErrNotFound, id)
		} else if err != nil {
			return err
		}
		if closed.Valid {
			return fmt.Errorf("%w: run %d is already closed", ErrValidation, id)
		}
		if _, err := tx.Exec(`UPDATE run SET closed_at = ? WHERE id = ?`, now(), id); err != nil {
			return err
		}
		_, err = addEvent(tx, eventSpec{projectID: projectID, runID: &id, actor: actor, kind: "run_closed"})
		return err
	})
}
