package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

type Host struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Fingerprint string `json:"fingerprint"`
}
type Source struct {
	ID     string `json:"id"`
	HostID string `json:"hostId"`
	Path   string `json:"path"`
	Parser string `json:"parser"`
}
type Store struct{ db *sql.DB }

func ID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// Pre-create with private permissions before SQLite opens the file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS hosts (id TEXT PRIMARY KEY, name TEXT NOT NULL, address TEXT NOT NULL, port INTEGER NOT NULL, username TEXT NOT NULL, fingerprint TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS sources (id TEXT PRIMARY KEY, host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE, path TEXT NOT NULL, UNIQUE(host_id,path));
	CREATE TABLE IF NOT EXISTS server_groups (id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE);
	CREATE TABLE IF NOT EXISTS group_hosts (group_id TEXT NOT NULL REFERENCES server_groups(id) ON DELETE CASCADE, host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE, PRIMARY KEY(group_id,host_id));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	var hasParser int
	if err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('sources') WHERE name='parser'").Scan(&hasParser); err == nil && hasParser == 0 {
		_, err = db.Exec("ALTER TABLE sources ADD COLUMN parser TEXT NOT NULL DEFAULT 'regular'")
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) List() ([]Host, []Source, error) {
	hosts := []Host{}
	sources := []Source{}
	rows, err := s.db.Query("SELECT id,name,address,port,username,fingerprint FROM hosts ORDER BY rowid")
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var h Host
		if err = rows.Scan(&h.ID, &h.Name, &h.Address, &h.Port, &h.Username, &h.Fingerprint); err != nil {
			rows.Close()
			return nil, nil, err
		}
		hosts = append(hosts, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = s.db.Query("SELECT id,host_id,path,parser FROM sources ORDER BY rowid")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var src Source
		if err = rows.Scan(&src.ID, &src.HostID, &src.Path, &src.Parser); err != nil {
			return nil, nil, err
		}
		sources = append(sources, src)
	}
	return hosts, sources, rows.Err()
}
func (s *Store) Add(h Host, paths []string) ([]Source, error) {
	return s.AddWithParser(h, paths, "regular")
}
func (s *Store) AddWithParser(h Host, paths []string, parser string) ([]Source, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO hosts VALUES (?,?,?,?,?,?)", h.ID, h.Name, h.Address, h.Port, h.Username, h.Fingerprint)
	if err != nil {
		return nil, err
	}
	sources := []Source{}
	for _, path := range paths {
		src := Source{ID: ID(), HostID: h.ID, Path: path, Parser: parser}
		if _, err = tx.Exec("INSERT INTO sources(id,host_id,path,parser) VALUES (?,?,?,?)", src.ID, src.HostID, src.Path, src.Parser); err != nil {
			return nil, err
		}
		sources = append(sources, src)
	}
	return sources, tx.Commit()
}
func (s *Store) Delete(id string) error {
	_, err := s.db.Exec("DELETE FROM hosts WHERE id=?", id)
	return err
}
