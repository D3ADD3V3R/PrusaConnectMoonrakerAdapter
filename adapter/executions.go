package moonraker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
	"pcma.local/moonraker/gcode"
)

// executionStore holds receipts and reservations for commands dispatched by PC.
type executionStore struct{ db *sql.DB }

type executionRecord struct {
	ExecutionID string
	Printer     string
	Path        string
	State       string
}

func openExecutions(filename string) (*executionStore, error) {
	if filename == "" || filename == ":memory:" {
		return nil, fmt.Errorf("starts require a persistent state_path")
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS artifacts (
 execution TEXT PRIMARY KEY, path TEXT NOT NULL, document TEXT NOT NULL, verified INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS executions (
 id INTEGER PRIMARY KEY AUTOINCREMENT, execution TEXT NOT NULL UNIQUE,
 printer TEXT NOT NULL, path TEXT NOT NULL, state TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_execution ON executions(printer)
 WHERE state IN ('START_REQUESTED','ACKNOWLEDGED','UNCERTAIN');
CREATE TABLE IF NOT EXISTS bed_confirmations (
 printer TEXT NOT NULL, generation TEXT NOT NULL, sequence INTEGER NOT NULL,
 consumed_by TEXT, PRIMARY KEY(printer,sequence));`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	var hasVerified int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('artifacts') WHERE name='verified'`).Scan(&hasVerified); err != nil {
		_ = db.Close()
		return nil, err
	}
	if hasVerified == 0 {
		if _, err := db.Exec(`ALTER TABLE artifacts ADD COLUMN verified INTEGER NOT NULL DEFAULT 0`); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &executionStore{db: db}, nil
}

func (s *executionStore) claim(ctx context.Context, name string, artifact gcode.Artifact) error {
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO artifacts(execution,path,document) VALUES(?,?,?) ON CONFLICT DO NOTHING`, artifact.ExecutionID, name, string(encoded)); err != nil {
		return err
	}
	var storedPath, stored string
	if err := s.db.QueryRowContext(ctx, `SELECT path,document FROM artifacts WHERE execution=?`, artifact.ExecutionID).Scan(&storedPath, &stored); err != nil {
		return err
	}
	if storedPath != name || stored != string(encoded) {
		return fmt.Errorf("execution ID already belongs to a different artifact")
	}
	return nil
}

func (s *executionStore) register(ctx context.Context, name string, artifact gcode.Artifact) error {
	if err := s.claim(ctx, name, artifact); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE artifacts SET verified=1 WHERE execution=?`, artifact.ExecutionID)
	return err
}

func (s *executionStore) artifact(ctx context.Context, name string) (gcode.Artifact, error) {
	var encoded string
	if err := s.db.QueryRowContext(ctx, `SELECT document FROM artifacts WHERE path=? AND verified=1`, name).Scan(&encoded); err != nil {
		return gcode.Artifact{}, err
	}
	var artifact gcode.Artifact
	err := json.Unmarshal([]byte(encoded), &artifact)
	return artifact, err
}

func (s *executionStore) latest(ctx context.Context, printer string) (executionRecord, error) {
	var record executionRecord
	err := s.db.QueryRowContext(ctx, `SELECT execution,printer,path,state FROM executions WHERE printer=? AND state!='REFUSED' ORDER BY id DESC LIMIT 1`, printer).Scan(&record.ExecutionID, &record.Printer, &record.Path, &record.State)
	return record, err
}

func (s *executionStore) find(ctx context.Context, execution string) (executionRecord, error) {
	var record executionRecord
	err := s.db.QueryRowContext(ctx, `SELECT execution,printer,path,state FROM executions WHERE execution=?`, execution).Scan(&record.ExecutionID, &record.Printer, &record.Path, &record.State)
	return record, err
}

func (s *executionStore) setState(ctx context.Context, execution, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE executions SET state=? WHERE execution=? AND state NOT IN ('FINISHED','FAILED','CANCELLED','REFUSED')`, state, execution)
	return err
}

func (s *executionStore) confirm(ctx context.Context, printer, generation string, sequence int64) error {
	if sequence < 1 {
		return fmt.Errorf("local bed confirmation needs a positive persistent sequence")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO bed_confirmations(printer,generation,sequence) VALUES(?,?,?) ON CONFLICT DO NOTHING`, printer, generation, sequence)
	if err != nil {
		return err
	}
	var recorded string
	if err := s.db.QueryRowContext(ctx, `SELECT generation FROM bed_confirmations WHERE printer=? AND sequence=?`, printer, sequence).Scan(&recorded); err != nil {
		return err
	}
	if recorded != generation {
		return fmt.Errorf("bed confirmation sequence belongs to an older generation")
	}
	return nil
}

func (s *executionStore) reserve(ctx context.Context, printer, name string, artifact gcode.Artifact, generation string, sequence int64, deviceExecution string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var lastExecution, lastState string
	err = tx.QueryRowContext(ctx, `SELECT execution,state FROM executions WHERE printer=? AND state!='REFUSED' ORDER BY id DESC LIMIT 1`, printer).Scan(&lastExecution, &lastState)
	if err != nil && !missingRecord(err) {
		return err
	}
	if lastExecution == "" {
		if generation != "INITIAL" || deviceExecution != "" {
			return fmt.Errorf("initial local bed confirmation is missing")
		}
	} else if !terminalExecution(lastState) || generation != lastExecution || deviceExecution != lastExecution {
		return fmt.Errorf("previous execution has no matching terminal bed confirmation")
	}
	var maxConsumed int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM bed_confirmations WHERE printer=? AND consumed_by IS NOT NULL`, printer).Scan(&maxConsumed); err != nil {
		return err
	}
	if sequence <= maxConsumed {
		return fmt.Errorf("bed confirmation sequence is stale")
	}
	changed, err := tx.ExecContext(ctx, `UPDATE bed_confirmations SET consumed_by=? WHERE printer=? AND generation=? AND sequence=? AND consumed_by IS NULL`, artifact.ExecutionID, printer, generation, sequence)
	if err != nil {
		return err
	}
	n, err := changed.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("bed confirmation has already been consumed")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO executions(execution,printer,path,state) VALUES(?,?,?,'START_REQUESTED')`, artifact.ExecutionID, printer, name); err != nil {
		return err
	}
	return tx.Commit()
}

func terminalExecution(state string) bool {
	return state == "FINISHED" || state == "FAILED" || state == "CANCELLED" || state == "REFUSED"
}

func (s *executionStore) refuse(ctx context.Context, execution string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed, err := tx.ExecContext(ctx, `UPDATE executions SET state='REFUSED' WHERE execution=? AND state='START_REQUESTED'`, execution)
	if err != nil {
		return err
	}
	n, err := changed.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("execution state changed before its refusal was recorded")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bed_confirmations SET consumed_by=NULL WHERE consumed_by=?`, execution); err != nil {
		return err
	}
	return tx.Commit()
}

func missingRecord(err error) bool { return errors.Is(err, sql.ErrNoRows) }
