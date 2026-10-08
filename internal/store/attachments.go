package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// MaxAttachment is the per-file upload limit.
const MaxAttachment = 20 << 20

// AddAttachment stores a file (deduplicated by content hash) and links it to
// a case, optionally within a run.
func (s *Store) AddAttachment(caseID int64, runID *int64, filename string, r io.Reader, actor Actor) (*Attachment, error) {
	b, err := s.saveBlob(filename, r)
	if err != nil {
		return nil, err
	}
	filename, mt, hash, path, data := b.filename, b.mime, b.sha, b.path, b.data

	var a *Attachment
	err = s.tx(func(tx *sql.Tx) error {
		c, err := getCase(tx, caseID)
		if err != nil {
			return err
		}
		evID, err := addEvent(tx, eventSpec{projectID: c.ProjectID, caseID: &caseID, runID: runID, actor: actor, kind: "attachment",
			data: map[string]any{"filename": filename, "size": len(data)}})
		if err != nil {
			return err
		}
		t := now()
		res, err := tx.Exec(`INSERT INTO attachment(case_id, run_id, event_id, filename, mime, size, sha256, path, created_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, caseID, runID, evID, filename, mt, len(data), hash, path, t)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		a = &Attachment{ID: id, CaseID: caseID, RunID: runID, Filename: filename, Mime: mt, Size: int64(len(data)),
			SHA256: hash, Path: path, CreatedAt: t}
		// Point the event at the attachment so the timeline can link it.
		_, err = tx.Exec(`UPDATE event SET data_json = json_set(data_json, '$.attachment_id', ?) WHERE id = ?`, id, evID)
		return err
	})
	return a, err
}

const attCols = `id, case_id, run_id, filename, mime, size, sha256, path, created_at`

func scanAttachment(r scanner) (*Attachment, error) {
	var a Attachment
	var runID sql.NullInt64
	if err := r.Scan(&a.ID, &a.CaseID, &runID, &a.Filename, &a.Mime, &a.Size, &a.SHA256, &a.Path, &a.CreatedAt); err != nil {
		return nil, err
	}
	if runID.Valid {
		a.RunID = &runID.Int64
	}
	return &a, nil
}

func (s *Store) GetAttachment(id int64) (*Attachment, error) {
	a, err := scanAttachment(s.db.QueryRow(`SELECT `+attCols+` FROM attachment WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: attachment %d", ErrNotFound, id)
	}
	return a, err
}

func (s *Store) caseAttachments(caseID int64) ([]Attachment, error) {
	rows, err := s.db.Query(`SELECT `+attCols+` FROM attachment WHERE case_id = ? ORDER BY id`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

type blob struct {
	filename, mime, sha, path string
	data                      []byte
}

// saveBlob validates an upload and stores it content-addressed under AttachDir.
func (s *Store) saveBlob(filename string, r io.Reader) (*blob, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxAttachment+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxAttachment {
		return nil, fmt.Errorf("%w: attachment larger than 20 MB", ErrValidation)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: attachment is empty", ErrValidation)
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		filename = "attachment"
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	ext := strings.ToLower(filepath.Ext(filename))
	mt := mime.TypeByExtension(ext)
	if mt == "" || ext == ".log" {
		mt = http.DetectContentType(data)
	}
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(strings.Split(mt, ";")[0]); len(exts) > 0 {
			ext = exts[0]
		}
	}
	if err := os.MkdirAll(s.AttachDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(s.AttachDir, hash+ext)
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
	}

	return &blob{filename: filename, mime: mt, sha: hash, path: path, data: data}, nil
}
