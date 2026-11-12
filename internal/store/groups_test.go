package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestGroupsMigratePersistAndCascadeWithoutDeletingServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE hosts (id TEXT PRIMARY KEY,name TEXT NOT NULL,address TEXT NOT NULL,port INTEGER NOT NULL,username TEXT NOT NULL,fingerprint TEXT NOT NULL);
	CREATE TABLE sources (id TEXT PRIMARY KEY,host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,path TEXT NOT NULL,UNIQUE(host_id,path));
	INSERT INTO hosts VALUES ('one','api','localhost',22,'user','pinned'); INSERT INTO sources VALUES ('source','one','/api.log');`)
	legacy.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, legacySources, err := s.List()
	if err != nil || len(legacySources) != 1 || legacySources[0].Parser != "regular" {
		t.Fatal("legacy source parser not migrated")
	}
	if _, err = s.Add(Host{ID: "two", Name: "worker", Address: "localhost", Port: 22, Username: "user", Fingerprint: "pin"}, []string{"/worker.log"}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []Group{{ID: "prod", Name: "Production", HostIDs: []string{"one", "two", "one"}}, {ID: "pay", Name: "Payments", HostIDs: []string{"one"}}, {ID: "empty", Name: "Empty", HostIDs: []string{}}} {
		if err = s.SaveGroup(g, true); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.SaveGroup(Group{ID: "duplicate", Name: "production"}, true); !errors.Is(err, ErrGroupNameExists) {
		t.Fatalf("duplicate group accepted: %v", err)
	}
	if err = s.SaveGroup(Group{ID: "prod", Name: "Broken", HostIDs: []string{"missing"}}, false); !errors.Is(err, ErrGroupHostMissing) {
		t.Fatalf("missing host accepted: %v", err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	groups, err := s.Groups()
	if err != nil || len(groups) != 3 || groups[2].Name != "Production" || len(groups[2].HostIDs) != 2 || len(groups[0].HostIDs) != 0 {
		t.Fatalf("membership did not persist or rollback: %+v %v", groups, err)
	}
	if err = s.SaveGroup(Group{ID: "prod", Name: "Live", HostIDs: []string{"two"}}, false); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteGroup("prod"); err != nil {
		t.Fatal(err)
	}
	hosts, sources, err := s.List()
	if err != nil || len(hosts) != 2 || len(sources) != 2 {
		t.Fatal("deleting group affected servers or sources")
	}
	if err = s.Delete("one"); err != nil {
		t.Fatal(err)
	}
	groups, err = s.Groups()
	if err != nil || len(groups) != 2 || len(groups[1].HostIDs) != 0 {
		t.Fatalf("deleted host membership not removed: %+v %v", groups, err)
	}
	if err = s.DeleteGroup("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing deletion accepted")
	}
	if err = s.SaveGroup(Group{ID: "missing", Name: "Missing"}, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("update created missing group")
	}
}
