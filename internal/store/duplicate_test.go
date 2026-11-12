package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestDuplicateCopiesSettingsWithIndependentIDsAndMembership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	host := Host{ID: "original", Name: "Payments", Address: "example.test", Port: 2222, Username: "vault@target", Fingerprint: "pinned"}
	sources, err := s.Add(host, []string{"/one.log", "/two.log"})
	if err != nil {
		t.Fatal(err)
	}
	sources[0].Parser = "tx"
	if err = s.UpdateSource(sources[0]); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveGroup(Group{ID: "prod", Name: "Production", HostIDs: []string{host.ID}}, true); err != nil {
		t.Fatal(err)
	}
	copy, added, err := s.DuplicateHost(host.ID, "Payments (copy)")
	if err != nil {
		t.Fatal(err)
	}
	if copy.ID == host.ID || copy.Name != "Payments (copy)" || copy.Address != host.Address || copy.Fingerprint != host.Fingerprint || copy.Username != host.Username || copy.Port != host.Port {
		t.Fatal("connection settings not copied")
	}
	if len(added) != 2 {
		t.Fatal("source count changed")
	}
	for i, source := range added {
		if source.ID == sources[i].ID || source.HostID != copy.ID || source.Path != sources[i].Path || source.Parser != sources[i].Parser {
			t.Fatal("sources lost configuration or identity isolation")
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	groups, err := s.Groups()
	if err != nil || len(groups) != 1 || len(groups[0].HostIDs) != 2 {
		t.Fatal("copied group membership did not persist")
	}
	added[0].Path = "/different.log"
	if err = s.UpdateSource(added[0]); err != nil {
		t.Fatal(err)
	}
	_, all, err := s.List()
	if err != nil || all[0] != sources[0] {
		t.Fatal("editing copy affected original")
	}
	if _, _, err = s.DuplicateHost("missing", "Missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing host duplicated")
	}
}

func TestDuplicateRollsBackIfSourceCopyFails(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Add(Host{ID: "original", Name: "Original"}, []string{"/log"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_source_copy BEFORE INSERT ON sources BEGIN SELECT RAISE(ABORT,'fixture failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.DuplicateHost("original", "Copy"); err == nil {
		t.Fatal("failed copy reported success")
	}
	hosts, sources, err := s.List()
	if err != nil || len(hosts) != 1 || len(sources) != 1 {
		t.Fatal("partial duplicate was saved")
	}
}
