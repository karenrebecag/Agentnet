//go:build unix

package expose

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karenrebecag/agentnet/internal/core"
	"github.com/karenrebecag/agentnet/internal/guests"
)

type fixture struct {
	t       *testing.T
	store   core.Store
	guests  string
	srv     *Server
	http    *httptest.Server
	stopped error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, store: core.Store{Home: dir}, guests: filepath.Join(dir, "guests.json")}
	f.srv = NewServer(ServerConfig{
		Store: f.store, GuestsPath: f.guests, AuditPath: filepath.Join(dir, "audit.jsonl"),
		Expires: time.Now().Add(time.Hour), Stop: func(err error) { f.stopped = err },
	})
	f.http = httptest.NewServer(nil)
	f.srv.cfg.Host = strings.TrimPrefix(f.http.URL, "http://")
	f.http.Config.Handler = f.srv.Handler()
	ctx, cancel := context.WithCancel(context.Background())
	go f.srv.Pump(ctx)
	t.Cleanup(func() { cancel(); f.http.Close() })
	return f
}

func (f *fixture) addGuest(name string, approved bool, targets ...string) string {
	token, err := guests.Add(f.guests, name, targets)
	if err != nil {
		f.t.Fatal(err)
	}
	if approved {
		if err := guests.Approve(f.guests, name); err != nil {
			f.t.Fatal(err)
		}
	}
	return token
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func (f *fixture) connect(token string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	return client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: f.http.URL, HTTPClient: &http.Client{Transport: bearer{token}}, MaxRetries: -1,
	}, nil)
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args any, out any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	if !res.IsError && out != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		json.Unmarshal(raw, out)
	}
	return text, res.IsError
}

func rawStatus(t *testing.T, target string, mutate func(*http.Request)) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	mutate(req)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestRejectsWrongHostAndBrowserOrigin(t *testing.T) {
	f := newFixture(t)
	token := f.addGuest("alice", true)
	auth := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	if got := rawStatus(t, f.http.URL, func(r *http.Request) { auth(r); r.Host = "evil.example" }); got != http.StatusMisdirectedRequest {
		t.Fatalf("wrong host: %d", got)
	}
	if got := rawStatus(t, f.http.URL, func(r *http.Request) { auth(r); r.Header.Set("Origin", "https://evil.example") }); got != http.StatusForbidden {
		t.Fatalf("origin: %d", got)
	}
}

func TestUnknownTokenIsRefused(t *testing.T) {
	f := newFixture(t)
	f.addGuest("alice", true)
	if got := rawStatus(t, f.http.URL, func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }); got != http.StatusUnauthorized {
		t.Fatalf("status %d", got)
	}
}

func TestAuthFailuresLockOneAddressWithoutEndingTheSession(t *testing.T) {
	f := newFixture(t)
	token := f.addGuest("alice", true)
	from := func(ip, tok string) int {
		return rawStatus(t, f.http.URL, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+tok)
			r.Header.Set("CF-Connecting-IP", ip)
		})
	}
	for range FailuresBeforeLock {
		from("198.51.100.1", "nope")
	}
	if got := from("198.51.100.1", token); got != http.StatusUnauthorized {
		t.Fatalf("locked address with a valid token: %d", got)
	}
	if got := from("198.51.100.2", token); got == http.StatusUnauthorized || got == http.StatusServiceUnavailable {
		t.Fatalf("other address refused: %d", got)
	}
	if f.stopped != nil {
		t.Fatalf("a stranger ended the session: %v", f.stopped)
	}
}

func TestParallelCallsPerGuestAreCapped(t *testing.T) {
	f := newFixture(t)
	f.store.Register("bob", "test", true)
	token := f.addGuest("alice", true, "bob")
	cs, _ := f.connect(token)
	defer cs.Close()
	var sent ReplyOutput
	call(t, cs, "ask", map[string]any{"to": "bob", "text": "x", "wait_seconds": 1}, &sent)
	results := make(chan string, inflightPerGuest+2)
	for range inflightPerGuest + 2 {
		go func() {
			text, _ := call(t, cs, "get_reply", map[string]any{"id": sent.ID, "wait_seconds": 3}, nil)
			results <- text
		}()
	}
	busy := 0
	for range inflightPerGuest + 2 {
		if strings.Contains(<-results, "too many requests") {
			busy++
		}
	}
	if busy == 0 {
		t.Fatal("no call was refused past the in-flight cap")
	}
}

func TestUnapprovedGuestStaysPendingUntilLocalApproval(t *testing.T) {
	f := newFixture(t)
	token := f.addGuest("alice", false, "bob")
	cs, err := f.connect(token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if text, isErr := call(t, cs, "list_agents", map[string]any{}, nil); !isErr || !strings.Contains(text, "pending approval") {
		t.Fatalf("pending call = %q, error=%v", text, isErr)
	}
	listed, _ := guests.List(f.guests)
	if !listed[0].Pending {
		t.Fatal("guest not marked pending")
	}
	guests.Approve(f.guests, "alice")
	if _, isErr := call(t, cs, "list_agents", map[string]any{}, nil); isErr {
		t.Fatal("approved guest still refused")
	}
}

func TestAskReachesOnlyOptedInTargetsAndReturnsReply(t *testing.T) {
	f := newFixture(t)
	token := f.addGuest("alice", true, "bob", "carol")
	f.store.Register("bob", "test", true)
	f.store.Register("carol", "test", false) // listed but did not opt in
	f.store.Register("dave", "test", true)   // opted in but not listed
	cs, err := f.connect(token)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	var list ListOutput
	call(t, cs, "list_agents", map[string]any{}, &list)
	if len(list.Agents) != 1 || list.Agents[0].Name != "bob" {
		t.Fatalf("list = %+v", list.Agents)
	}
	for _, to := range []string{"carol", "dave"} {
		if _, isErr := call(t, cs, "ask", map[string]any{"to": to, "text": "hi"}, nil); !isErr {
			t.Fatalf("ask %s accepted", to)
		}
	}

	go func() {
		for {
			batch, _ := f.store.Peek("bob")
			for _, m := range batch.Messages {
				if m.Origin != core.OriginExternal {
					t.Errorf("origin = %q", m.Origin)
				}
				f.store.SendWith(m.From, "bob", "pong", core.SendOptions{ReplyTo: m.ID})
			}
			f.store.Commit("bob", batch)
			if len(batch.Messages) > 0 {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	var reply ReplyOutput
	call(t, cs, "ask", map[string]any{"to": "bob", "text": "ping", "wait_seconds": 5}, &reply)
	if reply.Status != "answered" || reply.Reply != "pong" || reply.From != "bob" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestGetReplyHidesOtherGuestsIDs(t *testing.T) {
	f := newFixture(t)
	aliceTok := f.addGuest("alice", true, "bob")
	eveTok := f.addGuest("eve", true, "bob")
	f.store.Register("bob", "test", true)
	alice, _ := f.connect(aliceTok)
	defer alice.Close()
	eve, _ := f.connect(eveTok)
	defer eve.Close()
	var sent ReplyOutput
	call(t, alice, "ask", map[string]any{"to": "bob", "text": "secret", "wait_seconds": 1}, &sent)
	if sent.ID == "" || sent.Status != "waiting" {
		t.Fatalf("ask = %+v", sent)
	}
	text, isErr := call(t, eve, "get_reply", map[string]any{"id": sent.ID, "wait_seconds": 1}, nil)
	if !isErr || !strings.Contains(text, "unknown id") {
		t.Fatalf("eve read alice's id: %q", text)
	}
}

func TestSessionEndsAtExpiry(t *testing.T) {
	f := newFixture(t)
	f.srv.cfg.Expires = time.Now().Add(-time.Second)
	u, _ := url.Parse(f.http.URL)
	if got := rawStatus(t, u.String(), func(*http.Request) {}); got != http.StatusServiceUnavailable {
		t.Fatalf("status %d", got)
	}
	if !errors.Is(f.stopped, ErrTTL) {
		t.Fatalf("stopped = %v", f.stopped)
	}
}
