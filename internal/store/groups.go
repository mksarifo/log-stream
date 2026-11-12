package store

import (
	"database/sql"
	"errors"

	"github.com/mattn/go-sqlite3"
)

type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	HostIDs []string `json:"hostIds"`
}

var ErrGroupNameExists = errors.New("a group already uses this name")
var ErrGroupHostMissing = errors.New("a selected host no longer exists")

func (s *Store) Groups() ([]Group, error) {
	rows, err := s.db.Query(`SELECT g.id,g.name,m.host_id FROM server_groups g LEFT JOIN group_hosts m ON m.group_id=g.id ORDER BY g.name COLLATE NOCASE,g.id,m.host_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		var id, name string
		var host sql.NullString
		if err := rows.Scan(&id, &name, &host); err != nil {
			return nil, err
		}
		if len(groups) == 0 || groups[len(groups)-1].ID != id {
			groups = append(groups, Group{ID: id, Name: name, HostIDs: []string{}})
		}
		if host.Valid {
			groups[len(groups)-1].HostIDs = append(groups[len(groups)-1].HostIDs, host.String)
		}
	}
	return groups, rows.Err()
}

// SaveGroup replaces membership atomically, without changing SSH configuration.
func (s *Store) SaveGroup(group Group, create bool) (err error) {
	defer func() {
		var constraint sqlite3.Error
		if errors.As(err, &constraint) {
			switch constraint.ExtendedCode {
			case sqlite3.ErrConstraintUnique:
				err = ErrGroupNameExists
			case sqlite3.ErrConstraintForeignKey:
				err = ErrGroupHostMissing
			}
		}
	}()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if create {
		_, err = tx.Exec("INSERT INTO server_groups(id,name) VALUES (?,?)", group.ID, group.Name)
	} else {
		var result sql.Result
		result, err = tx.Exec("UPDATE server_groups SET name=? WHERE id=?", group.Name, group.ID)
		if err == nil {
			var count int64
			count, err = result.RowsAffected()
			if err == nil && count == 0 {
				return ErrNotFound
			}
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM group_hosts WHERE group_id=?", group.ID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range group.HostIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err = tx.Exec("INSERT INTO group_hosts(group_id,host_id) VALUES (?,?)", group.ID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteGroup(id string) error {
	result, err := s.db.Exec("DELETE FROM server_groups WHERE id=?", id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return err
}
