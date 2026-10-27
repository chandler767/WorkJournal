package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
)

// OpenReadOnly opens the existing database at path for reading only
// (mode=ro and query_only), so it can run beside the writer (serve). The
// schema is not applied; a missing database is an error.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: path,
		RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	if err := tempImports(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	if err := tempNotes(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open read-only %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// tempImports stands in an empty temporary imports table when the database
// predates it (serve has not run since the upgrade), so the meeting queries
// work; it lives in memory and writes nothing to the file.
func tempImports(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='imports'`).Scan(&n); err != nil || n > 0 {
		return err
	}
	if _, err := db.Exec(`PRAGMA query_only=0`); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE TEMP TABLE imports (id TEXT PRIMARY KEY, status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL)`)
	if _, qerr := db.Exec(`PRAGMA query_only=1`); err == nil {
		err = qerr
	}
	return err
}

// tempNotes is tempImports for the notes table.
func tempNotes(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='notes'`).Scan(&n); err != nil || n > 0 {
		return err
	}
	if _, err := db.Exec(`PRAGMA query_only=0`); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE TEMP TABLE notes (id INTEGER PRIMARY KEY, kind TEXT NOT NULL, day TEXT NOT NULL, start TEXT NOT NULL DEFAULT '',
		end TEXT NOT NULL DEFAULT '', text TEXT NOT NULL, source TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)
	if _, qerr := db.Exec(`PRAGMA query_only=1`); err == nil {
		err = qerr
	}
	return err
}
