//go:build unix

package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/karenrebecag/agentnet/internal/core"
)

func TestGapDoesNotAckOrWrite(t *testing.T) {
	conn := &scriptConn{in: [][]byte{
		mustJSON(t, map[string]any{"type": "welcome", "node": "laptop"}),
		mustJSON(t, map[string]any{"type": "deliver", "seq": 4, "envelope": map[string]any{
			"id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "from": "desktop.other", "to": "laptop.claude", "text": "one", "origin": "relay",
		}}),
		mustJSON(t, map[string]any{"type": "deliver", "seq": 7, "envelope": map[string]any{
			"id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "from": "desktop.other", "to": "laptop.claude", "text": "two", "origin": "relay",
		}}),
	}}
	n := 0
	store := core.Store{Home: t.TempDir()}
	err := Run(context.Background(), Options{
		Store: store,
		File:  File{URL: "http://example.test", Node: "laptop", Token: "an_secret"},
		Poll:  time.Hour,
		Sleep: func(context.Context, time.Duration) error { return nil },
		Dial: func(context.Context, string, string) (Conn, error) {
			n++
			if n == 1 {
				return conn, nil
			}
			return nil, errRevoked
		},
	})
	if !errors.Is(err, errRevoked) {
		t.Fatalf("err = %v", err)
	}
	var acked []int64
	for _, raw := range conn.out {
		var f struct {
			Type string `json:"type"`
			Seq  int64  `json:"seq"`
		}
		if json.Unmarshal(raw, &f) != nil {
			t.Fatalf("frame %s", raw)
		}
		if f.Type == "ack" {
			acked = append(acked, f.Seq)
		}
	}
	if len(acked) != 1 || acked[0] != 4 {
		t.Fatalf("acks = %v", acked)
	}
	batch, err := store.Peek("claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Messages) != 1 || batch.Messages[0].Text != "one" || batch.Messages[0].ID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("inbox %+v", batch.Messages)
	}
}

func TestLinkDeliverDedupesAndLeavesAdminAlone(t *testing.T) {
	const token = "an_secret"
	const relayID = "0123456789abcdef0123456789abcdef"
	var hello announced
	var acks []int64
	srv, done, errCh := newLinkServer(t, token, func(ctx context.Context, c *websocket.Conn) error {
		if err := wsWrite(ctx, c, map[string]string{"type": "welcome", "node": "laptop"}); err != nil {
			return err
		}
		var frame struct {
			Type   string      `json:"type"`
			Agents []announced `json:"agents"`
		}
		if err := wsRead(ctx, c, &frame); err != nil {
			return err
		}
		if frame.Type != "hello" || len(frame.Agents) != 1 {
			return errors.New("expected hello with one agent")
		}
		hello = frame.Agents[0]
		env := map[string]string{
			"id": relayID, "from": "desktop.other", "to": "laptop.claude", "text": "ping", "origin": "relay",
		}
		for _, seq := range []int64{4, 5} {
			if err := wsWrite(ctx, c, map[string]any{"type": "deliver", "seq": seq, "envelope": env}); err != nil {
				return err
			}
			var ack struct {
				Type string `json:"type"`
				Seq  int64  `json:"seq"`
			}
			if err := wsRead(ctx, c, &ack); err != nil {
				return err
			}
			if ack.Type != "ack" {
				return errors.New("expected ack")
			}
			acks = append(acks, ack.Seq)
		}
		return nil
	})
	defer srv.Close()

	home := t.TempDir()
	store := core.Store{Home: home}
	if err := store.Register("claude", "claude", true); err != nil {
		t.Fatal(err)
	}
	admin := filepath.Join(home, "relay-admin.json")
	if err := os.WriteFile(admin, []byte("{\"token\":\"do-not-touch\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(admin)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, Options{
			Store: store,
			File:  File{URL: srv.URL, Node: "laptop", Token: token},
			Poll:  time.Hour,
		})
	}()
	waitScript(t, done, errCh)
	cancel()
	<-runErr

	if hello.Name != "claude" || hello.Kind != "claude" || !hello.AcceptExternal {
		t.Fatalf("hello %+v", hello)
	}
	if len(acks) != 2 || acks[0] != 4 || acks[1] != 5 {
		t.Fatalf("acks %v", acks)
	}
	batch, err := store.Peek("claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Messages) != 1 || batch.Messages[0].ID != relayID || batch.Messages[0].Origin != OriginRelay {
		t.Fatalf("inbox %+v", batch.Messages)
	}
	after, _ := os.ReadFile(admin)
	if !bytes.Equal(before, after) {
		t.Fatal("admin file changed")
	}
}

func TestLinkWritesRejectionAndCommits(t *testing.T) {
	store := core.Store{Home: t.TempDir()}
	if err := store.Register("alice", "claude", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Send("other.bob", "alice", "hola"); err != nil {
		t.Fatal(err)
	}
	conn := &replyConn{}
	n := 0
	err := Run(context.Background(), Options{
		Store: store,
		File:  File{URL: "http://example.test", Node: "laptop", Token: "an_secret"},
		Poll:  time.Hour,
		Sleep: func(context.Context, time.Duration) error { return nil },
		Dial: func(context.Context, string, string) (Conn, error) {
			n++
			if n == 1 {
				return conn, nil
			}
			return nil, errRevoked
		},
	})
	if !errors.Is(err, errRevoked) {
		t.Fatalf("err = %v", err)
	}
	var send struct {
		Type, Ref, From, To, Text string
	}
	saw := false
	for _, raw := range conn.out {
		if json.Unmarshal(raw, &send) == nil && send.Type == "send" {
			saw = true
			break
		}
	}
	if !saw || send.From != "alice" || send.To != "other.bob" || send.Text != "hola" || send.Ref == "" {
		t.Fatalf("send %+v", send)
	}

	left, err := store.Peek("other.bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(left.Messages) != 0 {
		t.Fatalf("remote inbox not committed: %+v", left.Messages)
	}
	got, err := store.Peek("alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Text != "rejected: unknown node" || got.Messages[0].Origin != OriginRelay || got.Messages[0].From != "laptop" {
		t.Fatalf("rejection %+v", got.Messages)
	}
}

func newLinkServer(t *testing.T, token string, script func(context.Context, *websocket.Conn) error) (*httptest.Server, <-chan struct{}, <-chan error) {
	t.Helper()
	done := make(chan struct{})
	errCh := make(chan error, 1)
	first := make(chan struct{}, 1)
	first <- struct{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-first:
		default:
			c, err := websocket.Accept(w, r, nil)
			if err == nil {
				c.Close(websocket.StatusNormalClosure, "")
			}
			return
		}
		if r.URL.Path != "/v1/link" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "no", http.StatusUnauthorized)
			errCh <- errors.New("bad handshake")
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		c.SetReadLimit(128 << 10)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := script(ctx, c); err != nil {
			errCh <- err
			return
		}
		close(done)
	}))
	return srv, done, errCh
}

func waitScript(t *testing.T, done <-chan struct{}, errCh <-chan error) {
	t.Helper()
	select {
	case <-done:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("link script did not finish")
	}
}

func wsWrite(ctx context.Context, c *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, raw)
}

func wsRead(ctx context.Context, c *websocket.Conn, v any) error {
	_, raw, err := c.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type scriptConn struct {
	in  [][]byte
	i   int
	out [][]byte
}

func (s *scriptConn) Read(ctx context.Context) ([]byte, error) {
	if s.i < len(s.in) {
		b := s.in[s.i]
		s.i++
		return b, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *scriptConn) Write(_ context.Context, p []byte) error {
	s.out = append(s.out, append([]byte(nil), p...))
	return nil
}

func (s *scriptConn) Close() error { return nil }

// replyConn answers the forwarded send on the read that follows it.
// The session writes hello and send before that read, so the ref is already known.
type replyConn struct {
	step int
	out  [][]byte
}

func (c *replyConn) Write(_ context.Context, p []byte) error {
	c.out = append(c.out, append([]byte(nil), p...))
	return nil
}

func (c *replyConn) Read(context.Context) ([]byte, error) {
	switch c.step {
	case 0:
		c.step++
		return json.Marshal(map[string]string{"type": "welcome", "node": "laptop"})
	case 1:
		c.step++
		var ref string
		for _, raw := range c.out {
			var f struct {
				Type, Ref string
			}
			json.Unmarshal(raw, &f)
			if f.Type == "send" {
				ref = f.Ref
			}
		}
		if ref == "" {
			return nil, errors.New("send missing")
		}
		return json.Marshal(map[string]string{
			"type": "sent", "ref": ref, "status": "rejected", "error": "unknown node",
		})
	default:
		return nil, io.EOF
	}
}

func (c *replyConn) Close() error { return nil }
