package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) CreateProject(key, name, repo string) (*Project, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: project key is required", ErrValidation)
	}
	if name == "" {
		name = key
	}
	var p *Project
	err := s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO project(key, name, repo_path, created_at) VALUES (?,?,?,?)`, key, name, repo, now())
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("%w: project %q already exists", ErrValidation, key)
			}
			return err
		}
		id, _ := res.LastInsertId()
		p = &Project{ID: id, Key: key, Name: name, RepoPath: repo}
		_, err = addEvent(tx, eventSpec{projectID: id, actor: ActorClaude, kind: "project_created", data: map[string]any{"key": key}})
		return err
	})
	return p, err
}

func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT id, key, name, repo_path, created_at FROM project ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Key, &p.Name, &p.RepoPath, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) projectByKey(q querier, key string) (*Project, error) {
	var p Project
	err := q.QueryRow(`SELECT id, key, name, repo_path, created_at FROM project WHERE key = ?`, key).
		Scan(&p.ID, &p.Key, &p.Name, &p.RepoPath, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: project %q", ErrNotFound, key)
	}
	return &p, err
}

// ResolveProject finds a project by key; an empty key selects the only
// project, and is an error when there are zero or several.
func (s *Store) ResolveProject(key string) (*Project, error) {
	if key != "" {
		return s.projectByKey(s.db, key)
	}
	ps, err := s.ListProjects()
	if err != nil {
		return nil, err
	}
	switch len(ps) {
	case 0:
		return nil, fmt.Errorf("%w: no projects yet (run: qa project add <key>)", ErrNotFound)
	case 1:
		return &ps[0], nil
	}
	keys := make([]string, len(ps))
	for i, p := range ps {
		keys[i] = p.Key
	}
	return nil, fmt.Errorf("%w: several projects exist, pass --project (%s)", ErrValidation, strings.Join(keys, ", "))
}

func (s *Store) ListAreas(projectID int64) ([]Area, error) {
	rows, err := s.db.Query(`SELECT id, project_id, key, name, sort_order FROM area WHERE project_id = ? ORDER BY sort_order, key`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Area{}
	for rows.Next() {
		var a Area
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Key, &a.Name, &a.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
