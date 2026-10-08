// Package store holds every qa-tracker business rule. It is the only package
// that writes SQL; the server and CLI are thin adapters over it.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS project (
	id INTEGER PRIMARY KEY,
	key TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL,
	repo_path TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS area (
	id INTEGER PRIMARY KEY,
	project_id INTEGER NOT NULL REFERENCES project(id),
	key TEXT NOT NULL,
	name TEXT NOT NULL,
	sort_order INTEGER NOT NULL DEFAULT 0,
	UNIQUE(project_id, key)
);
CREATE TABLE IF NOT EXISTS test_case (
	id INTEGER PRIMARY KEY,
	project_id INTEGER NOT NULL REFERENCES project(id),
	area_id INTEGER NOT NULL REFERENCES area(id),
	key TEXT NOT NULL,
	title TEXT NOT NULL,
	preconditions TEXT NOT NULL DEFAULT '',
	steps_json TEXT NOT NULL DEFAULT '[]',
	expected TEXT NOT NULL DEFAULT '',
	priority TEXT NOT NULL CHECK (priority IN ('P0','P1','P2','P3')),
	tags TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'untested',
	severity TEXT,
	version INTEGER NOT NULL DEFAULT 1,
	reopen_count INTEGER NOT NULL DEFAULT 0,
	archived INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(project_id, key)
);
CREATE TABLE IF NOT EXISTS case_version (
	id INTEGER PRIMARY KEY,
	case_id INTEGER NOT NULL REFERENCES test_case(id),
	version INTEGER NOT NULL,
	title TEXT NOT NULL,
	preconditions TEXT NOT NULL,
	steps_json TEXT NOT NULL,
	expected TEXT NOT NULL,
	priority TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run (
	id INTEGER PRIMARY KEY,
	project_id INTEGER NOT NULL REFERENCES project(id),
	name TEXT NOT NULL DEFAULT '',
	build TEXT NOT NULL DEFAULT '',
	filter_json TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	closed_at TEXT
);
CREATE TABLE IF NOT EXISTS run_case (
	run_id INTEGER NOT NULL REFERENCES run(id),
	case_id INTEGER NOT NULL REFERENCES test_case(id),
	case_version INTEGER NOT NULL,
	result TEXT CHECK (result IN ('pass','fail','blocked','skip')),
	remarks TEXT NOT NULL DEFAULT '',
	severity TEXT,
	executed_at TEXT,
	PRIMARY KEY (run_id, case_id)
);
CREATE TABLE IF NOT EXISTS event (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id INTEGER NOT NULL REFERENCES project(id),
	case_id INTEGER REFERENCES test_case(id),
	run_id INTEGER REFERENCES run(id),
	actor TEXT NOT NULL CHECK (actor IN ('user','claude')),
	kind TEXT NOT NULL,
	from_status TEXT NOT NULL DEFAULT '',
	to_status TEXT NOT NULL DEFAULT '',
	data_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS attachment (
	id INTEGER PRIMARY KEY,
	case_id INTEGER NOT NULL REFERENCES test_case(id),
	run_id INTEGER REFERENCES run(id),
	event_id INTEGER REFERENCES event(id),
	filename TEXT NOT NULL,
	mime TEXT NOT NULL,
	size INTEGER NOT NULL,
	sha256 TEXT NOT NULL,
	path TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS idea (
	id INTEGER PRIMARY KEY,
	project_id INTEGER NOT NULL REFERENCES project(id),
	text TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'new',
	reopen_count INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS idea_attachment (
	id INTEGER PRIMARY KEY,
	idea_id INTEGER NOT NULL REFERENCES idea(id),
	filename TEXT NOT NULL,
	mime TEXT NOT NULL,
	size INTEGER NOT NULL,
	sha256 TEXT NOT NULL,
	path TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_event_project ON event(project_id, id);
CREATE INDEX IF NOT EXISTS idx_event_case ON event(case_id, id);
CREATE INDEX IF NOT EXISTS idx_case_status ON test_case(project_id, status);
`

// Store is a handle on one qa-tracker database.
type Store struct {
	db        *sql.DB
	AttachDir string
}

// Open opens (creating if needed) the database at path. Attachments live in
// an "attachments" directory beside it.
func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(abs) + "?" + url.Values{
		"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)"},
		"_txlock": {"immediate"},
	}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db, AttachDir: filepath.Join(filepath.Dir(abs), "attachments")}, nil
}

// migrate applies additive changes to databases created by older builds.
func migrate(db *sql.DB) error {
	has, err := hasColumn(db, "event", "idea_id")
	if err != nil {
		return err
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE event ADD COLUMN idea_id INTEGER REFERENCES idea(id)`); err != nil {
			return err
		}
	}
	_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_event_idea ON event(idea_id, id)`)
	return err
}

func hasColumn(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// tx runs fn in an immediate transaction (write lock taken up front, so two
// processes never deadlock upgrading read locks).
func (s *Store) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
	Exec(string, ...any) (sql.Result, error)
}
