package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// RecordResult records a manual execution result (spec §5/§6). With a RunID
// it also writes the run_case row; the run must be open and contain the case.
func (s *Store) RecordResult(caseID int64, in ResultInput, actor Actor) (*Case, error) {
	in.Remarks = strings.TrimSpace(in.Remarks)
	if !valid(Results, in.Result) {
		return nil, fmt.Errorf("%w: result %q must be one of %s", ErrValidation, in.Result, strings.Join(Results, ", "))
	}
	if in.Result == ResultFail {
		if in.Remarks == "" {
			return nil, fmt.Errorf("%w: a failure needs remarks describing what went wrong", ErrValidation)
		}
		if in.Severity == "" {
			in.Severity = "major"
		}
	}
	if in.Severity != "" && !valid(Severities, in.Severity) {
		return nil, fmt.Errorf("%w: severity %q must be one of %s", ErrValidation, in.Severity, strings.Join(Severities, ", "))
	}
	if in.Result != ResultFail && in.Result != ResultBlocked {
		in.Severity = ""
	}

	var out *Case
	err := s.tx(func(tx *sql.Tx) error {
		c, err := getCase(tx, caseID)
		if err != nil {
			return err
		}
		t := now()
		var build string
		if in.RunID != nil {
			var closed sql.NullString
			err := tx.QueryRow(`SELECT closed_at, build FROM run WHERE id = ? AND project_id = ?`, *in.RunID, c.ProjectID).Scan(&closed, &build)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: run %d not found", ErrValidation, *in.RunID)
			} else if err != nil {
				return err
			}
			if closed.Valid {
				return fmt.Errorf("%w: run %d is closed", ErrValidation, *in.RunID)
			}
			r, err := tx.Exec(`UPDATE run_case SET result=?, remarks=?, severity=NULLIF(?,''), executed_at=? WHERE run_id=? AND case_id=?`,
				in.Result, in.Remarks, in.Severity, t, *in.RunID, caseID)
			if err != nil {
				return err
			}
			if n, _ := r.RowsAffected(); n == 0 {
				return fmt.Errorf("%w: case %s is not part of run %d", ErrValidation, c.Key, *in.RunID)
			}
		}

		to := c.Status
		switch in.Result {
		case ResultPass:
			to = StatusPass
		case ResultFail:
			to = StatusFail
		case ResultBlocked:
			to = StatusBlocked
		}
		reopened := in.Result == ResultFail && c.Status == StatusFixed
		reopens := c.ReopenCount
		if reopened {
			reopens++
		}
		severity := c.Severity
		if in.Result != ResultSkip {
			severity = in.Severity
		}
		if _, err := tx.Exec(`UPDATE test_case SET status=?, severity=NULLIF(?,''), reopen_count=?, updated_at=? WHERE id=?`,
			to, severity, reopens, t, caseID); err != nil {
			return err
		}
		data := map[string]any{"result": in.Result}
		if in.Remarks != "" {
			data["remarks"] = in.Remarks
		}
		if in.Severity != "" {
			data["severity"] = in.Severity
		}
		if build != "" {
			data["build"] = build
		}
		if _, err := addEvent(tx, eventSpec{projectID: c.ProjectID, caseID: &caseID, runID: in.RunID, actor: actor,
			kind: "result", from: c.Status, to: to, data: data}); err != nil {
			return err
		}
		if reopened {
			if _, err := addEvent(tx, eventSpec{projectID: c.ProjectID, caseID: &caseID, runID: in.RunID, actor: actor,
				kind: "reopened", from: StatusFixed, to: StatusFail, data: map[string]any{"reopen_count": reopens}}); err != nil {
				return err
			}
		}
		out, err = getCase(tx, caseID)
		return err
	})
	return out, err
}

// transition moves a case from one of `from` to `to`, recording an event.
func (s *Store) transition(caseID int64, from []string, to, kind string, data map[string]any, actor Actor) (*Case, error) {
	var out *Case
	err := s.tx(func(tx *sql.Tx) error {
		c, err := getCase(tx, caseID)
		if err != nil {
			return err
		}
		if !valid(from, c.Status) {
			return fmt.Errorf("%w: cannot %s case %s while it is %q (allowed from: %s)",
				ErrInvalidTransition, kind, c.Key, c.Status, strings.Join(from, ", "))
		}
		if _, err := tx.Exec(`UPDATE test_case SET status=?, updated_at=? WHERE id=?`, to, now(), caseID); err != nil {
			return err
		}
		if _, err := addEvent(tx, eventSpec{projectID: c.ProjectID, caseID: &caseID, actor: actor, kind: kind,
			from: c.Status, to: to, data: data}); err != nil {
			return err
		}
		out, err = getCase(tx, caseID)
		return err
	})
	return out, err
}

// Claim marks an open bug as being worked on.
func (s *Store) Claim(caseID int64, actor Actor) (*Case, error) {
	return s.transition(caseID, []string{StatusFail, StatusBlocked}, StatusInFix, "claimed", nil, actor)
}

// MarkFixed records a fix and sends the case back for retest.
func (s *Store) MarkFixed(caseID int64, in FixInput, actor Actor) (*Case, error) {
	in.Note = strings.TrimSpace(in.Note)
	if in.Note == "" {
		return nil, fmt.Errorf("%w: a fix needs a note saying what changed and what to retest", ErrValidation)
	}
	data := map[string]any{"note": in.Note}
	if in.Commit != "" {
		data["commit"] = in.Commit
	}
	if len(in.Files) > 0 {
		data["files"] = in.Files
	}
	return s.transition(caseID, []string{StatusFail, StatusBlocked, StatusInFix}, StatusFixed, "fixed", data, actor)
}

// Comment adds a free-text note to the case timeline.
func (s *Store) Comment(caseID int64, text string, actor Actor) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("%w: comment is empty", ErrValidation)
	}
	return s.tx(func(tx *sql.Tx) error {
		c, err := getCase(tx, caseID)
		if err != nil {
			return err
		}
		_, err = addEvent(tx, eventSpec{projectID: c.ProjectID, caseID: &caseID, actor: actor, kind: "comment",
			data: map[string]any{"text": text}})
		return err
	})
}
