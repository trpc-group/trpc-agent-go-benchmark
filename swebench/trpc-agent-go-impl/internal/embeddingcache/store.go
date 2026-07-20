//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package embeddingcache

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const maxLookupKeys = 400

// Store is a concurrency-safe SQLite embedding cache shared by runner workers.
type Store struct {
	db             *sql.DB
	path           string
	identity       Identity
	identityDigest [32]byte
	writeMu        sync.Mutex
}

// ReadStats describes one cache lookup.
type ReadStats struct {
	BytesRead   int64
	Corruptions int64
	Duration    time.Duration
}

// WriteStats describes one cache write transaction.
type WriteStats struct {
	Rows         int64
	BytesWritten int64
	Duration     time.Duration
}

// Open creates or validates the SQLite database for one embedding identity.
func Open(ctx context.Context, directory string, identity Identity) (*Store, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("embedding cache directory is required")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve embedding cache directory: %w", err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create embedding cache directory: %w", err)
	}
	path := filepath.Join(directory, databaseFileName(identity))
	dsnURL := &url.URL{Scheme: "file", Path: path}
	query := dsnURL.Query()
	query.Set("_busy_timeout", "30000")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "NORMAL")
	query.Set("_txlock", "immediate")
	dsnURL.RawQuery = query.Encode()

	db, err := sql.Open("sqlite3", dsnURL.String())
	if err != nil {
		return nil, fmt.Errorf("open embedding cache: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(16)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping embedding cache: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure embedding cache database: %w", err)
	}

	store := &Store{
		db:             db,
		path:           path,
		identity:       identity.normalized(),
		identityDigest: identityHash(identity),
	}
	if err := store.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS cache_metadata (
			name TEXT PRIMARY KEY,
			value TEXT NOT NULL
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS embeddings (
			cache_key BLOB PRIMARY KEY,
			dimensions INTEGER NOT NULL,
			codec INTEGER NOT NULL,
			vector BLOB NOT NULL,
			checksum INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		) WITHOUT ROWID`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize embedding cache schema: %w", err)
		}
	}

	identityJSON, err := json.Marshal(s.identity)
	if err != nil {
		return fmt.Errorf("marshal embedding cache identity: %w", err)
	}
	expected := map[string]string{
		"schema_version": fmt.Sprintf("%d", schemaVersion),
		"identity_hash":  hex.EncodeToString(s.identityDigest[:]),
		"identity_json":  string(identityJSON),
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin embedding cache metadata transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for name, value := range expected {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO cache_metadata(name, value) VALUES(?, ?)`,
			name,
			value,
		); err != nil {
			return fmt.Errorf("write embedding cache metadata %q: %w", name, err)
		}
		var actual string
		if err := tx.QueryRowContext(
			ctx,
			`SELECT value FROM cache_metadata WHERE name = ?`,
			name,
		).Scan(&actual); err != nil {
			return fmt.Errorf("read embedding cache metadata %q: %w", name, err)
		}
		if actual != value {
			return fmt.Errorf(
				"embedding cache metadata mismatch for %s: got %q, want %q",
				name,
				actual,
				value,
			)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit embedding cache metadata: %w", err)
	}
	return nil
}

// Path returns the concrete SQLite database path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Dimensions returns the vector dimensions isolated by this store.
func (s *Store) Dimensions() int {
	if s == nil {
		return 0
	}
	return s.identity.Dimensions
}

// Key returns the content-addressed cache key for exact embedding input.
func (s *Store) Key(text string) Key {
	return keyForText(s.identityDigest, text)
}

// GetMany returns valid cached vectors and treats malformed rows as misses.
func (s *Store) GetMany(
	ctx context.Context,
	keys []Key,
) (values map[Key][]float64, stats ReadStats, err error) {
	started := time.Now()
	defer func() { stats.Duration = time.Since(started) }()
	values = make(map[Key][]float64)
	if len(keys) == 0 {
		return values, stats, nil
	}

	unique := uniqueKeys(keys)
	for offset := 0; offset < len(unique); offset += maxLookupKeys {
		end := offset + maxLookupKeys
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[offset:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for index, key := range chunk {
			placeholders[index] = "?"
			args[index] = append([]byte(nil), key[:]...)
		}
		query := `SELECT cache_key, dimensions, codec, vector, checksum
			FROM embeddings WHERE cache_key IN (` + strings.Join(placeholders, ",") + `)`
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, stats, fmt.Errorf("query embedding cache: %w", err)
		}
		for rows.Next() {
			var rawKey []byte
			var dimensions, codec int
			var data []byte
			var checksum int64
			if err := rows.Scan(&rawKey, &dimensions, &codec, &data, &checksum); err != nil {
				_ = rows.Close()
				return nil, stats, fmt.Errorf("scan embedding cache row: %w", err)
			}
			stats.BytesRead += int64(len(data))
			if len(rawKey) != len(Key{}) || checksum < 0 || checksum > int64(^uint32(0)) {
				stats.Corruptions++
				continue
			}
			var key Key
			copy(key[:], rawKey)
			if dimensions != s.identity.Dimensions {
				stats.Corruptions++
				continue
			}
			vector, err := decodeVector(dimensions, codec, data, uint32(checksum))
			if err != nil {
				stats.Corruptions++
				continue
			}
			values[key] = vector
		}
		if err := rows.Close(); err != nil {
			return nil, stats, fmt.Errorf("close embedding cache rows: %w", err)
		}
		if err := rows.Err(); err != nil {
			return nil, stats, fmt.Errorf("iterate embedding cache rows: %w", err)
		}
	}
	return values, stats, nil
}

// PutMany validates and atomically upserts vectors.
func (s *Store) PutMany(
	ctx context.Context,
	values map[Key][]float64,
) (stats WriteStats, err error) {
	started := time.Now()
	defer func() { stats.Duration = time.Since(started) }()
	if len(values) == 0 {
		return stats, nil
	}

	encoded := make(map[Key]encodedVector, len(values))
	for key, vector := range values {
		if len(vector) != s.identity.Dimensions {
			return stats, fmt.Errorf(
				"embedding cache vector dimension mismatch: got %d, want %d",
				len(vector),
				s.identity.Dimensions,
			)
		}
		value, err := encodeVector(vector)
		if err != nil {
			return stats, err
		}
		encoded[key] = value
		stats.BytesWritten += int64(len(value.Data))
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("begin embedding cache write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	statement, err := tx.PrepareContext(ctx, `INSERT INTO embeddings(
			cache_key, dimensions, codec, vector, checksum, created_at
		) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET
			dimensions = excluded.dimensions,
			codec = excluded.codec,
			vector = excluded.vector,
			checksum = excluded.checksum`)
	if err != nil {
		return stats, fmt.Errorf("prepare embedding cache write: %w", err)
	}
	defer statement.Close()
	createdAt := time.Now().UTC().Unix()
	for key, value := range encoded {
		if _, err := statement.ExecContext(
			ctx,
			key[:],
			value.Dimensions,
			value.Codec,
			value.Data,
			int64(value.Checksum),
			createdAt,
		); err != nil {
			return stats, fmt.Errorf("write embedding cache row: %w", err)
		}
		stats.Rows++
	}
	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("commit embedding cache write: %w", err)
	}
	return stats, nil
}

// Close closes the shared SQLite database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func uniqueKeys(keys []Key) []Key {
	seen := make(map[Key]struct{}, len(keys))
	unique := make([]Key, 0, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, key)
	}
	return unique
}
