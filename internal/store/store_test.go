package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPersistenceTransactionAndCascade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := Host{ID: "host", Name: "api", Address: "127.0.0.1", Port: 22, Username: "deploy", Fingerprint: "pinned"}
	if _, err = s.Add(h, []string{"/log", "/log"}); err == nil {
		t.Fatal("duplicate source must fail")
	}
	hosts, _, _ := s.List()
	if len(hosts) != 0 {
		t.Fatal("failed insert was not rolled back")
	}
	if _, err = s.Add(h, []string{"/api.log", "/worker.log"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hosts, sources, err := s.List()
	if err != nil || len(hosts) != 1 || len(sources) != 2 {
		t.Fatal("configuration did not persist")
	}
	if err = s.Delete(h.ID); err != nil {
		t.Fatal(err)
	}
	hosts, sources, _ = s.List()
	if len(hosts) != 0 || len(sources) != 0 {
		t.Fatal("sources did not cascade")
	}
}

func TestEditingPreservesIdentityAndRejectsDuplicateSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edit.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	host := Host{ID: "one", Name: "original", Address: "old.example", Port: 22, Username: "user", Fingerprint: "old"}
	sources, err := s.Add(host, []string{"/one.log", "/two.log"})
	if err != nil {
		t.Fatal(err)
	}
	other := Host{ID: "two", Name: "other", Address: "other.example", Port: 22, Username: "user", Fingerprint: "other"}
	if _, err = s.Add(other, []string{"/new.log"}); err != nil {
		t.Fatal(err)
	}
	host.Name = "edited"
	host.Address = "new.example"
	host.Port = 2222
	host.Username = "vault@target@server"
	host.Fingerprint = "new"
	if err = s.UpdateHost(host); err != nil {
		t.Fatal(err)
	}
	updated := sources[0]
	updated.Path = "/new.log"
	updated.Parser = "tx"
	if err = s.UpdateSource(updated); err != nil {
		t.Fatal(err)
	}
	duplicate := sources[1]
	duplicate.Path = "/new.log"
	if err = s.UpdateSource(duplicate); !errors.Is(err, ErrSourcePathExists) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	wrongOwner := updated
	wrongOwner.HostID = "two"
	wrongOwner.Path = "/moved.log"
	if err = s.UpdateSource(wrongOwner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("source moved across hosts: %v", err)
	}
	missing := host
	missing.ID = "missing"
	if err = s.UpdateHost(missing); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing host silently created")
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hosts, all, err := s.List()
	if err != nil || len(hosts) != 2 || len(all) != 3 {
		t.Fatal("configuration lost after editing")
	}
	if hosts[0] != host || all[0] != updated || all[1] != sources[1] {
		t.Fatalf("edit did not persist or changed another source: %+v %+v", hosts, all)
	}
}
