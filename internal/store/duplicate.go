package store

import (
	"database/sql"
	"errors"
)

// DuplicateHost copies configuration in one transaction. SSH workers and their
// in-memory credentials belong to the collector, and are never copied here.
func (s *Store) DuplicateHost(id, name string) (Host, []Source, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Host{}, nil, err
	}
	defer tx.Rollback()
	var host Host
	err = tx.QueryRow("SELECT id,name,address,port,username,fingerprint FROM hosts WHERE id=?", id).Scan(&host.ID, &host.Name, &host.Address, &host.Port, &host.Username, &host.Fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, nil, ErrNotFound
	}
	if err != nil {
		return Host{}, nil, err
	}
	rows, err := tx.Query("SELECT path,parser FROM sources WHERE host_id=? ORDER BY rowid", id)
	if err != nil {
		return Host{}, nil, err
	}
	sources := []Source{}
	host.ID, host.Name = ID(), name
	for rows.Next() {
		source := Source{ID: ID(), HostID: host.ID}
		if err = rows.Scan(&source.Path, &source.Parser); err != nil {
			rows.Close()
			return Host{}, nil, err
		}
		sources = append(sources, source)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Host{}, nil, err
	}
	if _, err = tx.Exec("INSERT INTO hosts(id,name,address,port,username,fingerprint) VALUES (?,?,?,?,?,?)", host.ID, host.Name, host.Address, host.Port, host.Username, host.Fingerprint); err != nil {
		return Host{}, nil, err
	}
	for _, source := range sources {
		if _, err = tx.Exec("INSERT INTO sources(id,host_id,path,parser) VALUES (?,?,?,?)", source.ID, source.HostID, source.Path, source.Parser); err != nil {
			return Host{}, nil, err
		}
	}
	if _, err = tx.Exec("INSERT INTO group_hosts(group_id,host_id) SELECT group_id,? FROM group_hosts WHERE host_id=?", host.ID, id); err != nil {
		return Host{}, nil, err
	}
	if err = tx.Commit(); err != nil {
		return Host{}, nil, err
	}
	return host, sources, nil
}
