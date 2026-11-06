//go:build unix

package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestNodeAddInboxFlag(t *testing.T) {
	var posts int
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		json.NewEncoder(w).Encode(map[string]string{"token": "an_issued"})
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("AGENTNET_HOME", home)
	writeRelayAdmin(t, home, srv.URL)

	var stderr bytes.Buffer
	if code := Run([]string{"relay", "node", "add", "laptop", "--kind", "machine", "--inbox"}, strings.NewReader(""), &bytes.Buffer{}, &stderr); code == 0 || posts != 0 {
		t.Fatalf("machine code %d posts %d %s", code, posts, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--inbox") {
		t.Fatalf("stderr %s", stderr.String())
	}
	if code := Run([]string{"relay", "node", "add", "web", "--kind", "guest", "--targets", "mac.claude", "--inbox"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 || posts != 1 {
		t.Fatalf("guest inbox code %d posts %d", code, posts)
	}
	if bodies[0]["inbox"] != true {
		t.Fatalf("body %+v", bodies[0])
	}
	if code := Run([]string{"relay", "node", "add", "web", "--kind", "guest", "--targets", "mac.claude"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 || posts != 2 {
		t.Fatalf("guest code %d posts %d", code, posts)
	}
	if _, ok := bodies[1]["inbox"]; ok {
		t.Fatalf("inbox key present %+v", bodies[1])
	}
}

func writeRelayAdmin(t *testing.T, home, rawURL string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"url": rawURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "relay.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "relay-admin.json"), []byte("{\"token\":\"admin-secret\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dirHasCLI(t *testing.T, root, needle string) bool {
	t.Helper()
	found := false
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(b), needle) {
			found = true
		}
		return nil
	})
	return found
}

func TestLinkWithoutConfigFails(t *testing.T) {
	t.Setenv("AGENTNET_HOME", t.TempDir())
	code := Run([]string{"link"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if code == 0 {
		t.Fatal("link without config")
	}
}
