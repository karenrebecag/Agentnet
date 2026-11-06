//go:build unix

package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitRejectsHTTPAndWrites0600(t *testing.T) {
	home := t.TempDir()
	if err := Init(home, "http://example.test"); err == nil {
		t.Fatal("http accepted")
	}
	if err := Init(home, "https://example.test/x"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, "relay.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	f, err := Load(home)
	if err != nil || f.URL != "https://example.test" {
		t.Fatalf("%+v %v", f, err)
	}
	dir, err := os.Stat(home)
	if err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dir.Mode().Perm())
	}
}

func TestAdminTokenFileIs0600(t *testing.T) {
	home := t.TempDir()
	tok, err := WriteAdminToken(home)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "an_") || len(tok) != 46 || strings.ContainsAny(tok, "+/=") {
		t.Fatalf("token shape len=%d", len(tok))
	}
	info, err := os.Stat(adminPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}
