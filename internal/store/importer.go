package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// validateImport checks the whole file up front so a bad file writes nothing.
func validateImport(f *ImportFile, existingAreas map[string]bool) error {
	var problems []string
	areas := map[string]bool{}
	for k := range existingAreas {
		areas[k] = true
	}
	for _, a := range f.Areas {
		if !keyRe.MatchString(a.Key) {
			problems = append(problems, fmt.Sprintf("area %q: invalid key", a.Key))
		}
		areas[a.Key] = true
	}
	seen := map[string]bool{}
	for i, c := range f.Cases {
		id := c.Key
		if id == "" {
			id = fmt.Sprintf("cases[%d]", i)
		}
		var errs []string
		if !keyRe.MatchString(c.Key) {
			errs = append(errs, "invalid key (lowercase a-z0-9 . _ -)")
		}
		if seen[c.Key] {
			errs = append(errs, "duplicate key")
		}
		seen[c.Key] = true
		if !areas[c.Area] {
			errs = append(errs, fmt.Sprintf("unknown area %q", c.Area))
		}
		if !valid(Priorities, c.Priority) {
			errs = append(errs, fmt.Sprintf("priority %q not in P0..P3", c.Priority))
		}
		if strings.TrimSpace(c.Title) == "" {
			errs = append(errs, "title is empty")
		}
		if len(c.Steps) == 0 {
			errs = append(errs, "no steps")
		}
		if strings.TrimSpace(c.Expected) == "" {
			errs = append(errs, "expected is empty")
		}
		if len(errs) > 0 {
			problems = append(problems, id+": "+strings.Join(errs, "; "))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: import rejected, nothing written:\n  %s", ErrValidation, strings.Join(problems, "\n  "))
	}
	return nil
}

// Import upserts areas and cases by key (spec §9). Content changes (steps,
// expected, preconditions) bump the version, snapshot the old one, and send
// the case back to untested; metadata changes keep status and version.
func (s *Store) Import(f ImportFile, archiveMissing bool, actor Actor) (*ImportResult, error) {
	p, err := s.projectByKey(s.db, f.Project)
	if err != nil {
		return nil, err
	}
	res := &ImportResult{}
	err = s.tx(func(tx *sql.Tx) error {
		existing := map[string]int64{}
		rows, err := tx.Query(`SELECT key, id FROM area WHERE project_id = ?`, p.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k string
			var id int64
			rows.Scan(&k, &id)
			existing[k] = id
		}
		rows.Close()
		known := map[string]bool{}
		for k := range existing {
			known[k] = true
		}
		if err := validateImport(&f, known); err != nil {
			return err
		}
		for _, a := range f.Areas {
			name := a.Name
			if name == "" {
				name = a.Key
			}
			if id, ok := existing[a.Key]; ok {
				if _, err := tx.Exec(`UPDATE area SET name = ?, sort_order = ? WHERE id = ?`, name, a.Order, id); err != nil {
					return err
				}
				continue
			}
			r, err := tx.Exec(`INSERT INTO area(project_id, key, name, sort_order) VALUES (?,?,?,?)`, p.ID, a.Key, name, a.Order)
			if err != nil {
				return err
			}
			existing[a.Key], _ = r.LastInsertId()
		}
		inFile := map[string]bool{}
		for _, ic := range f.Cases {
			inFile[ic.Key] = true
			if err := importCase(tx, p.ID, existing[ic.Area], ic, actor, res); err != nil {
				return fmt.Errorf("case %s: %w", ic.Key, err)
			}
		}
		if archiveMissing {
			rows, err := tx.Query(`SELECT id, key FROM test_case WHERE project_id = ? AND archived = 0`, p.ID)
			if err != nil {
				return err
			}
			var gone []int64
			for rows.Next() {
				var id int64
				var k string
				rows.Scan(&id, &k)
				if !inFile[k] {
					gone = append(gone, id)
				}
			}
			rows.Close()
			for _, id := range gone {
				if _, err := tx.Exec(`UPDATE test_case SET archived = 1, updated_at = ? WHERE id = ?`, now(), id); err != nil {
					return err
				}
				if _, err := addEvent(tx, eventSpec{projectID: p.ID, caseID: &id, actor: actor, kind: "case_archived"}); err != nil {
					return err
				}
				res.Archived++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func importCase(tx *sql.Tx, projectID, areaID int64, ic ImportCase, actor Actor, res *ImportResult) error {
	stepsJSON, _ := json.Marshal(ic.Steps)
	tags := strings.Join(ic.Tags, ",")
	t := now()

	cur, err := scanCase(tx.QueryRow(`SELECT `+caseCols+caseFrom+`WHERE c.project_id = ? AND c.key = ?`, projectID, ic.Key))
	if errors.Is(err, sql.ErrNoRows) {
		r, err := tx.Exec(`INSERT INTO test_case(project_id, area_id, key, title, preconditions, steps_json, expected, priority, tags, status, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, projectID, areaID, ic.Key, ic.Title, ic.Preconditions, string(stepsJSON), ic.Expected, ic.Priority, tags, StatusUntested, t, t)
		if err != nil {
			return err
		}
		id, _ := r.LastInsertId()
		res.Created++
		_, err = addEvent(tx, eventSpec{projectID: projectID, caseID: &id, actor: actor, kind: "case_created", to: StatusUntested})
		return err
	}
	if err != nil {
		return err
	}

	var content, meta []string
	if cur.Preconditions != ic.Preconditions {
		content = append(content, "preconditions")
	}
	if !slices.Equal(cur.Steps, ic.Steps) {
		content = append(content, "steps")
	}
	if cur.Expected != ic.Expected {
		content = append(content, "expected")
	}
	if cur.Title != ic.Title {
		meta = append(meta, "title")
	}
	if cur.Priority != ic.Priority {
		meta = append(meta, "priority")
	}
	if strings.Join(cur.Tags, ",") != tags {
		meta = append(meta, "tags")
	}
	if cur.AreaID != areaID {
		meta = append(meta, "area")
	}
	if cur.Archived {
		meta = append(meta, "unarchived")
	}
	if len(content) == 0 && len(meta) == 0 {
		res.Unchanged++
		return nil
	}

	version, status := cur.Version, cur.Status
	if len(content) > 0 {
		oldSteps, _ := json.Marshal(cur.Steps)
		if _, err := tx.Exec(`INSERT INTO case_version(case_id, version, title, preconditions, steps_json, expected, priority, created_at)
			VALUES (?,?,?,?,?,?,?,?)`, cur.ID, cur.Version, cur.Title, cur.Preconditions, string(oldSteps), cur.Expected, cur.Priority, t); err != nil {
			return err
		}
		version++
		if status != StatusUntested {
			status = StatusUntested
			res.Reset++
		}
	}
	if _, err := tx.Exec(`UPDATE test_case SET area_id=?, title=?, preconditions=?, steps_json=?, expected=?, priority=?, tags=?,
		status=?, version=?, archived=0, updated_at=? WHERE id=?`,
		areaID, ic.Title, ic.Preconditions, string(stepsJSON), ic.Expected, ic.Priority, tags, status, version, t, cur.ID); err != nil {
		return err
	}
	res.Updated++
	_, err = addEvent(tx, eventSpec{projectID: projectID, caseID: &cur.ID, actor: actor, kind: "case_updated",
		from: cur.Status, to: status,
		data: map[string]any{"changed": append(content, meta...), "version": version}})
	return err
}

// Export returns the project's non-archived cases in import format.
func (s *Store) Export(projectID int64) (*ImportFile, error) {
	var key string
	if err := s.db.QueryRow(`SELECT key FROM project WHERE id = ?`, projectID).Scan(&key); err != nil {
		return nil, fmt.Errorf("%w: project %d", ErrNotFound, projectID)
	}
	areas, err := s.ListAreas(projectID)
	if err != nil {
		return nil, err
	}
	cases, err := s.ListCases(projectID, CaseFilter{})
	if err != nil {
		return nil, err
	}
	f := &ImportFile{Project: key, Areas: []ImportArea{}, Cases: []ImportCase{}}
	for _, a := range areas {
		f.Areas = append(f.Areas, ImportArea{Key: a.Key, Name: a.Name, Order: a.SortOrder})
	}
	for _, c := range cases {
		f.Cases = append(f.Cases, ImportCase{Key: c.Key, Area: c.AreaKey, Title: c.Title, Priority: c.Priority,
			Tags: c.Tags, Preconditions: c.Preconditions, Steps: c.Steps, Expected: c.Expected})
	}
	return f, nil
}
