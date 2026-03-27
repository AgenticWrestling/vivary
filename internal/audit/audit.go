// Package audit persists every MUS frame that transits keeperd into a SQLite
// WAL table.  The schema is append-only; keeperd never deletes rows.
//
// The vivlog CLI reads this table via the same package to decode records.
package audit

import (
	"bytes"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
	"vivary.dev/vivary/internal/ctl"
)

// DB wraps a SQLite connection with the audit schema.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path, applies the schema, and
// returns a ready-to-use DB.  The WAL journal mode is set for write concurrency.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path+"?_journal=WAL")
	if err != nil {
		return nil, fmt.Errorf("audit: open %q: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit: set busy_timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("audit: apply schema: %w", err)
	}
	return &DB{db: db}, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS frames (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT    NOT NULL,          -- RFC3339Nano
    msg_type    TEXT    NOT NULL,
    from_id     TEXT    NOT NULL,
    to_id       TEXT    NOT NULL,
    seq_no      INTEGER NOT NULL,
    payload     BLOB                       -- NULL when audit-payload is false
);

CREATE INDEX IF NOT EXISTS idx_frames_from ON frames(from_id);
CREATE INDEX IF NOT EXISTS idx_frames_type ON frames(msg_type);
CREATE INDEX IF NOT EXISTS idx_frames_ts   ON frames(ts);

CREATE TABLE IF NOT EXISTS security_events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts      TEXT NOT NULL,
    agent   TEXT NOT NULL,
    kind    TEXT NOT NULL,
    detail  TEXT NOT NULL
);
`

// WriteFrame appends a frame record.  payload may be nil (omit sensitive data).
func (d *DB) WriteFrame(ts time.Time, msgType, fromID, toID string, seqNo uint64, payload []byte) error {
	_, err := d.db.Exec(
		`INSERT INTO frames(ts, msg_type, from_id, to_id, seq_no, payload)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ts.UTC().Format(time.RFC3339Nano),
		msgType, fromID, toID, seqNo, payload,
	)
	return err
}

// WriteSecurityEvent appends a security event.
func (d *DB) WriteSecurityEvent(ts time.Time, agent, kind, detail string) error {
	_, err := d.db.Exec(
		`INSERT INTO security_events(ts, agent, kind, detail) VALUES (?, ?, ?, ?)`,
		ts.UTC().Format(time.RFC3339Nano), agent, kind, detail,
	)
	return err
}

// ---- Record types returned by query methods --------------------------------

// FrameRecord is a decoded row from the frames table.
type FrameRecord struct {
	ID      int64
	Ts      time.Time
	MsgType string
	FromID  string
	ToID    string
	SeqNo   uint64
	Payload []byte // nil if not stored
}

// QueryFrames executes a flexible filter query.  All filter fields are
// optional; zero values are ignored.
type FrameFilter struct {
	AgentID string // matches from_id
	MsgType string // exact msg_type
	Since   time.Time
	Until   time.Time
	SeqNo   uint64 // exact seq_no, 0 = any
	Limit   int    // 0 = no limit
}

// SecurityEventRecord is a decoded row from the security_events table.
type SecurityEventRecord struct {
	ID     int64
	Ts     time.Time
	Agent  string
	Kind   string
	Detail string
}

// SecurityEventFilter returns security events matching the provided fields.
type SecurityEventFilter struct {
	Agent string
	Kind  string
	Limit int
}

// Tail returns the most recent n frames across all agents.
func (d *DB) Tail(n int) ([]FrameRecord, error) {
	rows, err := d.db.Query(
		`SELECT id, ts, msg_type, from_id, to_id, seq_no, payload
		 FROM frames ORDER BY id DESC LIMIT ?`, n,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFrames(rows)
}

// QueryFrames returns frames matching f.
func (d *DB) QueryFrames(f FrameFilter) ([]FrameRecord, error) {
	q := `SELECT id, ts, msg_type, from_id, to_id, seq_no, payload FROM frames WHERE 1=1`
	var args []any
	if f.AgentID != "" {
		q += " AND from_id = ?"
		args = append(args, f.AgentID)
	}
	if f.MsgType != "" {
		q += " AND msg_type = ?"
		args = append(args, f.MsgType)
	}
	if !f.Since.IsZero() {
		q += " AND ts >= ?"
		args = append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	if !f.Until.IsZero() {
		q += " AND ts <= ?"
		args = append(args, f.Until.UTC().Format(time.RFC3339Nano))
	}
	if f.SeqNo != 0 {
		q += " AND seq_no = ?"
		args = append(args, f.SeqNo)
	}
	q += " ORDER BY id ASC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFrames(rows)
}

func scanFrames(rows *sql.Rows) ([]FrameRecord, error) {
	var records []FrameRecord
	for rows.Next() {
		var r FrameRecord
		var tsStr string
		var payload []byte
		if err := rows.Scan(&r.ID, &tsStr, &r.MsgType, &r.FromID, &r.ToID, &r.SeqNo, &payload); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			return nil, fmt.Errorf("audit: parse timestamp %q: %w", tsStr, err)
		}
		r.Ts = t
		r.Payload = payload
		records = append(records, r)
	}
	return records, rows.Err()
}

// QuerySecurityEvents returns security events matching f.
func (d *DB) QuerySecurityEvents(f SecurityEventFilter) ([]SecurityEventRecord, error) {
	q := `SELECT id, ts, agent, kind, detail FROM security_events WHERE 1=1`
	var args []any
	if f.Agent != "" {
		q += " AND agent = ?"
		args = append(args, f.Agent)
	}
	if f.Kind != "" {
		q += " AND kind = ?"
		args = append(args, f.Kind)
	}
	q += " ORDER BY id ASC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []SecurityEventRecord
	for rows.Next() {
		var r SecurityEventRecord
		var tsStr string
		if err := rows.Scan(&r.ID, &tsStr, &r.Agent, &r.Kind, &r.Detail); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			return nil, fmt.Errorf("audit: parse timestamp %q: %w", tsStr, err)
		}
		r.Ts = t
		records = append(records, r)
	}
	return records, rows.Err()
}

// Close closes the underlying database connection.
func (d *DB) Close() error { return d.db.Close() }

// ---- Completion / Failure event helpers ------------------------------------

// CompletionEvent is the structured payload for MsgType_CompletionEvent.
type CompletionEvent = ctl.CompletionEventPayload

// FailureEvent is the structured payload for MsgType_FailureEvent.
type FailureEvent = ctl.FailureEventPayload

// MarshalEvent serialises an event into binary MUS format.
func MarshalEvent(v ctl.MUSPayload) ([]byte, error) {
	return v.MarshalMUS(), nil
}

// UnmarshalCompletion decodes a CompletionEvent from a binary MUS frame payload.
func UnmarshalCompletion(b []byte) (CompletionEvent, error) {
	var ev CompletionEvent
	err := ev.UnmarshalMUS(bytes.NewReader(b))
	return ev, err
}

// UnmarshalFailure decodes a FailureEvent from a binary MUS frame payload.
func UnmarshalFailure(b []byte) (FailureEvent, error) {
	var ev FailureEvent
	err := ev.UnmarshalMUS(bytes.NewReader(b))
	return ev, err
}
