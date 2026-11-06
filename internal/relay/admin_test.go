//go:build unix

package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	if _, _, err := AddNode(ctx, home, "phone", "guest", nil); err == nil {
		t.Fatal("guest without targets")
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
