package store

import (
	"errors"

	"github.com/mattn/go-sqlite3"
)

var ErrNotFound = errors.New("configuration not found")
var ErrSourcePathExists = errors.New("a source already uses this path on the host")

// UpdateHost preserves the primary key and every attached source.
func (s *Store) UpdateHost(h Host) error {
	result, err := s.db.Exec("UPDATE hosts SET name=?, address=?, port=?, username=?, fingerprint=? WHERE id=?", h.Name, h.Address, h.Port, h.Username, h.Fingerprint, h.ID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// The host ID is part of the predicate so a source cannot be moved by an edit.
func (s *Store) UpdateSource(source Source) error {
	result, err := s.db.Exec("UPDATE sources SET path=?,parser=? WHERE id=? AND host_id=?", source.Path, source.Parser, source.ID, source.HostID)
	if err != nil {
		var constraint sqlite3.Error
		if errors.As(err, &constraint) && constraint.ExtendedCode == sqlite3.ErrConstraintUnique {
			return ErrSourcePathExists
		}
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
