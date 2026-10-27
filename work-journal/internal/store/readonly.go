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
	return &Store{db: db}, nil
}
