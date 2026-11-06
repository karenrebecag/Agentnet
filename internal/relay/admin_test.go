//go:build unix

package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminRoundTripAndLocalFile(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admin-secret" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/nodes":
			posts++
			var body struct {
				Name, Kind string
				Targets    []string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode: %v", err)
			}
			if body.Kind == "guest" && len(body.Targets) == 0 {
				http.Error(w, "targets", http.StatusBadRequest)
				return
			}
			tok := "an_issuedlaptop"
			if body.Name == "desktop" {
				tok = "an_issueddesktop"
			}
			json.NewEncoder(w).Encode(map[string]string{"name": body.Name, "token": tok})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/nodes":
			json.NewEncoder(w).Encode([]Node{{
				Name: "laptop", Kind: "machine", Created: "t0", LastSeen: "t1",
				Agents: []NodeAgent{{Name: "claude", Kind: "claude"}},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/nodes/laptop/rotate":
			json.NewEncoder(w).Encode(map[string]string{"token": "an_rotated"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/nodes/laptop/revoke":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	home := t.TempDir()
	if err := Save(home, File{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(adminPath(home), []byte("{\"token\":\"admin-secret\"}\n")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := AddNode(ctx, home, "phone", "guest", []string{"not-an-address"}); err == nil {
		t.Fatal("guest with a malformed target")
	}
	if posts != 0 {
		t.Fatal("guest check hit the relay")
	}
	tok, saved, err := AddNode(ctx, home, "laptop", "machine", nil)
	if err != nil || !saved || tok != "an_issuedlaptop" {
		t.Fatalf("add %s saved=%v %v", tok, saved, err)
	}
	f, err := Load(home)
	if err != nil || f.Node != "laptop" || f.Token != tok {
		t.Fatalf("saved %+v %v", f, err)
	}
	tok, saved, err = AddNode(ctx, home, "desktop", "machine", nil)
	if err != nil || saved || tok != "an_issueddesktop" {
		t.Fatalf("second %s saved=%v %v", tok, saved, err)
	}
	f, _ = Load(home)
	if f.Node != "laptop" || f.Token != "an_issuedlaptop" {
		t.Fatalf("clobbered %+v", f)
	}
	nodes, err := ListNodes(ctx, home)
	if err != nil || len(nodes) != 1 || nodes[0].Name != "laptop" || nodes[0].Agents[0].Name != "claude" {
		t.Fatalf("%+v %v", nodes, err)
	}
	tok, err = RotateNode(ctx, home, "laptop")
	if err != nil || tok != "an_rotated" {
		t.Fatalf("rotate %s %v", tok, err)
	}
	f, _ = Load(home)
	if f.Token != "an_rotated" {
		t.Fatalf("token not updated %+v", f)
	}
	if err := RevokeNode(ctx, home, "laptop"); err != nil {
		t.Fatal(err)
	}
	f, _ = Load(home)
	if f.Token != "" || f.Node != "laptop" {
		t.Fatalf("revoke %+v", f)
	}
}

func TestAdminDoesNotFollowRedirect(t *testing.T) {
	hit := false
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			hit = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sink.Close)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+"/steal", http.StatusFound)
	}))
	t.Cleanup(src.Close)
	err := Client{BaseURL: src.URL, Token: "an_admin"}.Revoke(context.Background(), "laptop")
	if !errors.Is(err, errConfig) {
		t.Fatalf("err = %v", err)
	}
	if hit {
		t.Fatal("bearer reached the redirect target")
	}
}

func TestAdminCleartextIsRejected(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, File{URL: "http://example.test", Node: "laptop", Token: "an_node"}); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(adminPath(home), []byte("{\"token\":\"an_admin\"}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := ListNodes(context.Background(), home); !errors.Is(err, errConfig) {
		t.Fatalf("err = %v", err)
	}
}

func TestPairRejectsBadHost(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	home := adminFixture(t, srv.URL)
	bad := []string{
		"https://claude.ai",
		"claude.ai/mcp",
		"claude.ai?x=1",
		"not-a-user" + "@" + "claude.example",
		"127.0.0.1",
		"::1",
		"[::1]",
		"*",
		"*.claude.ai",
		"claude.ai:443",
		"1.2.3.4",
		"127.000.000.001",
		"2130706433",
		"0x7f000001",
		"localhost:1",
	}
	for _, host := range bad {
		_, err := PairNode(context.Background(), home, "web", host)
		if !errors.Is(err, errPairHost) {
			t.Fatalf("host %q err = %v", host, err)
		}
	}
	if posts != 0 {
		t.Fatalf("posts %d", posts)
	}
}

func TestPairCodeNotStored(t *testing.T) {
	const code = "K7NP-4QDM"
	const leak = "NEWLINESECRET"
	var gotFor string
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/nodes/web/pair" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer admin-secret" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		var body struct {
			For string `json:"for"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		gotFor = body.For
		w.WriteHeader(http.StatusCreated)
		switch mode {
		case "leak":
			json.NewEncoder(w).Encode(map[string]string{"code": leak + "\nmore", "expires": "nope", "connector_url": "http://relay.example.test/mcp"})
		case "clear":
			json.NewEncoder(w).Encode(map[string]string{"code": code, "expires": "2026-11-06T18:10:00Z", "connector_url": "http://relay.example.test/mcp"})
		default:
			json.NewEncoder(w).Encode(map[string]string{
				"code": code, "expires": "2026-11-06T18:10:00Z", "connector_url": "https://relay.example.test/mcp",
			})
		}
	}))
	t.Cleanup(srv.Close)
	home := adminFixture(t, srv.URL)
	before, err := os.ReadFile(filepath.Join(home, "relay.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := PairNode(context.Background(), home, "web", "Claude.AI.")
	if err != nil {
		t.Fatal(err)
	}
	if gotFor != "claude.ai" || p.Code != code || p.ConnectorURL != "https://relay.example.test/mcp" || p.Expires != "2026-11-06T18:10:00Z" {
		t.Fatalf("for %q pairing %+v", gotFor, p)
	}
	after, err := os.ReadFile(filepath.Join(home, "relay.json"))
	if err != nil || string(after) != string(before) || dirHas(t, home, code) {
		t.Fatal("pairing code touched disk")
	}
	mode = "leak"
	_, err = PairNode(context.Background(), home, "web", "localhost")
	if err == nil || strings.Contains(err.Error(), leak) || dirHas(t, home, leak) {
		t.Fatalf("err = %v", err)
	}
	if gotFor != "localhost" {
		t.Fatalf("for %q", gotFor)
	}
	mode = "clear"
	p, err = PairNode(context.Background(), home, "web", "claude.ai")
	if err != nil || p.Code != code || p.ConnectorURL != "" {
		t.Fatalf("cleartext connector %+v %v", p, err)
	}
}

func TestAddNodeOptsInbox(t *testing.T) {
	var posts int
	var saw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		saw = map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&saw); err != nil {
			t.Errorf("decode: %v", err)
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "an_issued"})
	}))
	t.Cleanup(srv.Close)
	home := adminFixture(t, srv.URL)
	ctx := context.Background()
	if _, _, err := AddNodeOpts(ctx, home, "laptop", "machine", nil, true); err == nil || posts != 0 {
		t.Fatalf("machine inbox posts=%d err=%v", posts, err)
	}
	if _, _, err := AddNodeOpts(ctx, home, "web", "guest", []string{"mac.claude"}, true); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || saw["inbox"] != true {
		t.Fatalf("posts %d body %+v", posts, saw)
	}
	posts = 0
	saw = nil
	if _, _, err := AddNodeOpts(ctx, home, "web", "guest", []string{"mac.claude"}, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := saw["inbox"]; ok || posts != 1 {
		t.Fatalf("inbox key present posts=%d body %+v", posts, saw)
	}
}

func adminFixture(t *testing.T, rawURL string) string {
	t.Helper()
	home := t.TempDir()
	if err := Save(home, File{URL: rawURL}); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(adminPath(home), []byte("{\"token\":\"admin-secret\"}\n")); err != nil {
		t.Fatal(err)
	}
	return home
}

func dirHas(t *testing.T, root, needle string) bool {
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
