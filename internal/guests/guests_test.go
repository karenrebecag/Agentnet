package guests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddCheckRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guests.json")
	token, err := Add(path, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), token) {
		t.Fatal("file stored the raw token")
	}
	got, err := Check(path, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "alice" {
		t.Fatalf("name %q", got.Name)
	}
	listed, err := List(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "alice" || listed[0].Revoked {
		t.Fatalf("list %+v", listed)
	}
	if _, err := Check(path, "nope"); err != ErrDenied {
		t.Fatalf("unknown token: %v", err)
	}
	if err := Revoke(path, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(path, token); err != ErrDenied {
		t.Fatalf("revoked token: %v", err)
	}
}

func TestRejectsBadName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guests.json")
	if _, err := Add(path, "../alice", nil); err != ErrName {
		t.Fatalf("path name: %v", err)
	}
	if _, err := Add(path, "Alice", nil); err != ErrName {
		t.Fatalf("uppercase name: %v", err)
	}
}
