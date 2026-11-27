package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite" // the SQLite driver, in pure Go

	"github.com/prabhask5/devbox/internal/proto"
)

// store is the hub's database: the sessions, so they survive a restart, and an append-only
// log of what happened, which the context generator reads.
type store struct {
	db                        *sql.DB
	putSession, deleteSession *sql.Stmt
	addEvent, eventsSince     *sql.Stmt
}

// Event is one entry of the event log.
type Event struct {
	At   int64          `json:"at"` // Unix ms
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

func openStore(ctx context.Context, dir string) (*store, error) {
	path := filepath.Join(dir, "hub.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; the hub serializes its own access
	s := &store{db: db}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, data TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS events (seq INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, type TEXT NOT NULL, data TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS events_at ON events (at)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	for target, query := range map[**sql.Stmt]string{
		&s.putSession:    `INSERT INTO sessions (id, data) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET data = excluded.data`,
		&s.deleteSession: `DELETE FROM sessions WHERE id = ?`,
		&s.addEvent:      `INSERT INTO events (at, type, data) VALUES (?, ?, ?)`,
		&s.eventsSince:   `SELECT at, type, data FROM events WHERE at >= ? ORDER BY seq`,
	} {
		if *target, err = db.PrepareContext(ctx, query); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *store) close() { _ = s.db.Close() }

func (s *store) sessions(ctx context.Context) ([]proto.Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read to the end below
	var sessions []proto.Session
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var session proto.Session
		if json.Unmarshal([]byte(data), &session) == nil {
			sessions = append(sessions, session)
		}
	}
	return sessions, rows.Err()
}

func (s *store) save(ctx context.Context, session *proto.Session) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	_, err = s.putSession.ExecContext(ctx, session.ID, string(data))
	return err
}

func (s *store) remove(ctx context.Context, id string) error {
	_, err := s.deleteSession.ExecContext(ctx, id)
	return err
}

func (s *store) log(ctx context.Context, at int64, kind string, data map[string]any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = s.addEvent.ExecContext(ctx, at, kind, string(encoded))
	return err
}

func (s *store) events(ctx context.Context, since int64) ([]Event, error) {
	rows, err := s.eventsSince.QueryContext(ctx, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read to the end below
	events := []Event{}
	for rows.Next() {
		var event Event
		var data string
		if err := rows.Scan(&event.At, &event.Type, &data); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(data), &event.Data)
		events = append(events, event)
	}
	return events, rows.Err()
}
