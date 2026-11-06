//go:build unix

package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRelayInitRejectsHTTP(t *testing.T) {
	t.Setenv("AGENTNET_HOME", t.TempDir())
	var stderr bytes.Buffer
	code := Run([]string{"relay", "init", "--url", "http://example.test"}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Fatal("http url accepted")
	}
}

func TestAdminTokenPrintsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENTNET_HOME", home)
	var stdout bytes.Buffer
	code := Run([]string{"relay", "admin-token"}, strings.NewReader(""), &stdout, &bytes.Buffer{})
	if code != 0 {
		t.Fatal(code)
	}
	tok := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(tok, "an_") || strings.Contains(tok, "\n") {
		t.Fatalf("printed %d bytes", len(tok))
	}
	info, err := os.Stat(home + "/relay-admin.json")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}

func TestSendToRelayAddressSaysViaRelay(t *testing.T) {
	t.Setenv("AGENTNET_HOME", t.TempDir())
	var stderr bytes.Buffer
	code := Run([]string{"send", "probe.mcp", "--from", "e2e", "--message", "ping"}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code != ExitOffline {
		t.Fatalf("code %d %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "via relay") || strings.Contains(stderr.String(), "offline") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestSendToOfflineLocalStillSaysOffline(t *testing.T) {
	t.Setenv("AGENTNET_HOME", t.TempDir())
	var stderr bytes.Buffer
	code := Run([]string{"send", "bob", "--from", "e2e", "--message", "ping"}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code != ExitOffline {
		t.Fatalf("code %d %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "offline, delivered on next join/read") {
		t.Fatalf("stderr %s", stderr.String())
	}
}

func TestLinkWithoutConfigFails(t *testing.T) {
	t.Setenv("AGENTNET_HOME", t.TempDir())
	code := Run([]string{"link"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if code == 0 {
		t.Fatal("link without config")
	}
}
