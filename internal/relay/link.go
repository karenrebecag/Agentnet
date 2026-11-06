//go:build unix

package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/karenrebecag/agentnet/internal/core"
)

const (
	closeReplaced = websocket.StatusCode(4000)
	closeRevoked  = websocket.StatusCode(4001)
)

var (
	errRevoked  = errors.New("relay token rejected")
	errReplaced = errors.New("link replaced by another session")
	errConfig   = errors.New("relay config rejected")
	errGap      = errors.New("relay sequence gap")
	errPoll     = errors.New("poll")
	// A redirect can move the bearer from https onto cleartext on the same host.
	errRedirect = errors.New("relay redirect refused")
)

// Conn is the websocket surface the session needs. Tests substitute it.
type Conn interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, p []byte) error
	Close() error
}

type announced struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	AcceptExternal bool   `json:"accept_external"`
	// Session changes with every join, so the relay can tie an approval to
	// one process instead of to a name the next process may reuse.
	Session string `json:"session,omitempty"`
}

type incoming struct {
	Type     string         `json:"type"`
	Node     string         `json:"node,omitempty"`
	Seq      *int64         `json:"seq,omitempty"`
	Envelope *core.Envelope `json:"envelope,omitempty"`
	Ref      string         `json:"ref,omitempty"`
	Status   string         `json:"status,omitempty"`
	Error    string         `json:"error,omitempty"`
	Code     string         `json:"code,omitempty"`
}

type outbound struct {
	inbox string
	batch core.Batch
	index int
	ref   string
}

// Options configures one link process. File comes from relay.json.
// Dial, Sleep and Poll are optional; tests set them.
type Options struct {
	Store core.Store
	File  File
	Dial  func(ctx context.Context, rawURL, token string) (Conn, error)
	Sleep func(ctx context.Context, d time.Duration) error
	Poll  time.Duration
}

// Run keeps a session until ctx is cancelled. A rejected token or a replaced
// socket stops the process. Every other error reconnects, with backoff.
func Run(ctx context.Context, opt Options) error {
	if opt.File.URL == "" || opt.File.Node == "" || opt.File.Token == "" {
		return errors.New("relay.json needs url, node and token")
	}
	if !nodeRE.MatchString(opt.File.Node) {
		return fmt.Errorf("%w: %q", errConfig, opt.File.Node)
	}
	dial := opt.Dial
	if dial == nil {
		dial = defaultDial
	}
	sleep := opt.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	poll := opt.Poll
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		welcomed, err := runOnce(ctx, opt, dial, poll)
		if ctx.Err() != nil {
			return nil
		}
		if isFatal(err) {
			return err
		}
		if welcomed {
			backoff = time.Second
		}
		if err := sleep(ctx, backoff); err != nil {
			return nil
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func isFatal(err error) bool {
	return errors.Is(err, errRevoked) || errors.Is(err, errReplaced) || errors.Is(err, errConfig)
}

func runOnce(ctx context.Context, opt Options, dial func(context.Context, string, string) (Conn, error), poll time.Duration) (bool, error) {
	conn, err := dial(ctx, opt.File.URL, opt.File.Token)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	// The pending read lives as long as this connection, not one poll.
	readCtx, stopReading := context.WithCancel(ctx)
	defer stopReading()
	s := &session{
		conn: conn, store: opt.Store, node: opt.File.Node,
		seen: map[string]struct{}{}, poll: poll,
		readCtx: readCtx, frames: make(chan frame, 1),
	}
	in, err := s.readFor(ctx, 0)
	if err != nil {
		return false, err
	}
	if in.Type != "welcome" || in.Node != s.node {
		return false, fmt.Errorf("%w: welcome node", errConfig)
	}
	if err := s.announce(ctx, "hello"); err != nil {
		return true, err
	}
	if err := s.pumpOut(ctx); err != nil {
		return true, err
	}
	for {
		in, err := s.readFor(ctx, s.poll)
		if errors.Is(err, errPoll) {
			if err := s.announce(ctx, "agents"); err != nil {
				return true, err
			}
			if err := s.pumpOut(ctx); err != nil {
				return true, err
			}
			continue
		}
		if err != nil {
			return true, err
		}
		if err := s.on(ctx, in); err != nil {
			return true, err
		}
	}
}

type session struct {
	conn      Conn
	store     core.Store
	node      string
	seen      map[string]struct{}
	poll      time.Duration
	have      bool
	next      int64
	announced []announced
	out       *outbound
	rr        int
	readCtx   context.Context
	frames    chan frame
	reading   bool
}

type frame struct {
	b   []byte
	err error
}

func (s *session) on(ctx context.Context, in incoming) error {
	switch in.Type {
	case "deliver":
		return s.onDeliver(ctx, in)
	case "sent":
		if err := s.onSent(in); err != nil {
			return err
		}
		return s.flush(ctx)
	case "error":
		if fatalCode(in.Code) {
			return errRevoked
		}
		return fmt.Errorf("relay error %s", in.Code)
	default:
		return nil
	}
}

func fatalCode(code string) bool {
	switch code {
	case "unauthorized", "revoked", "rotated":
		return true
	default:
		return false
	}
}

func (s *session) onDeliver(ctx context.Context, in incoming) error {
	if in.Seq == nil || in.Envelope == nil || *in.Seq <= 0 {
		return errors.New("bad deliver")
	}
	seq := *in.Seq
	// The relay flushes the same unacked seq again when hello arrives. A seq
	// we already acked is a replay, not a gap. Acking it again is a no-op once
	// the relay has recorded the first ack, and repairs a lost one.
	if s.have && seq < s.next {
		return s.ack(ctx, seq)
	}
	if s.have && seq != s.next {
		return errGap
	}
	env := *in.Envelope
	if env.ID == "" || !s.known(env.ID) {
		node, agent, err := ParseAddress(env.To)
		if err != nil || node != s.node {
			return errors.New("deliver not for this node")
		}
		if err := s.store.Append(agent, env); err != nil {
			return err
		}
		if env.ID != "" {
			s.seen[env.ID] = struct{}{}
		}
	}
	if err := s.ack(ctx, seq); err != nil {
		return err
	}
	s.have = true
	s.next = seq + 1
	return nil
}

func (s *session) known(id string) bool {
	_, ok := s.seen[id]
	return ok
}

func (s *session) onSent(in incoming) error {
	if s.out == nil || in.Ref == "" || in.Ref != s.out.ref {
		return errors.New("unexpected sent")
	}
	msg := s.out.batch.Messages[s.out.index]
	switch in.Status {
	case "delivered", "queued":
	case "rejected":
		if err := s.writeRejection(msg.From, in.Error); err != nil {
			return err
		}
	default:
		return fmt.Errorf("send status %q", in.Status)
	}
	s.out.index++
	s.out.ref = ""
	return nil
}

func (s *session) writeRejection(sender, why string) error {
	if core.ValidateName(sender) != nil {
		return nil
	}
	if why == "" {
		why = "rejected"
	}
	id, err := core.NewID()
	if err != nil {
		return err
	}
	return s.store.Append(sender, core.Envelope{
		ID: id, From: s.node, To: sender, Text: "rejected: " + why, Origin: OriginRelay,
	})
}

func (s *session) pumpOut(ctx context.Context) error {
	if s.out != nil {
		return s.flush(ctx)
	}
	name, batch, ok, err := s.pick()
	if err != nil || !ok {
		return err
	}
	s.out = &outbound{inbox: name, batch: batch}
	return s.flush(ctx)
}

func (s *session) flush(ctx context.Context) error {
	if s.out == nil {
		return nil
	}
	for s.out.index < len(s.out.batch.Messages) && s.out.ref == "" {
		msg := s.out.batch.Messages[s.out.index]
		if msg.Corrupt || msg.Text == "" || msg.From == "" {
			s.out.index++
			continue
		}
		ref, err := core.NewID()
		if err != nil {
			return err
		}
		s.out.ref = ref
		frame := map[string]string{
			"type": "send", "ref": ref, "from": msg.From, "to": s.out.inbox, "text": msg.Text,
		}
		if msg.ReplyTo != "" {
			frame["reply_to"] = msg.ReplyTo
		}
		return s.write(ctx, frame)
	}
	if s.out.ref != "" {
		return nil
	}
	inbox, batch := s.out.inbox, s.out.batch
	s.out = nil
	return s.store.Commit(inbox, batch)
}

func (s *session) pick() (string, core.Batch, bool, error) {
	names, err := s.store.Inboxes()
	if err != nil {
		return "", core.Batch{}, false, err
	}
	var remote []string
	for _, name := range names {
		node, _, err := ParseAddress(name)
		if err != nil || node == s.node {
			continue
		}
		remote = append(remote, name)
	}
	if len(remote) == 0 {
		return "", core.Batch{}, false, nil
	}
	start := s.rr % len(remote)
	for i := 0; i < len(remote); i++ {
		name := remote[(start+i)%len(remote)]
		batch, err := s.store.Peek(name)
		if err != nil {
			return "", core.Batch{}, false, err
		}
		if len(batch.Messages) == 0 {
			continue
		}
		s.rr = (start + i + 1) % len(remote)
		return name, batch, true, nil
	}
	return "", core.Batch{}, false, nil
}

func (s *session) announce(ctx context.Context, kind string) error {
	cur := s.live()
	if kind != "hello" && sameAgents(cur, s.announced) {
		return nil
	}
	s.announced = cur
	if cur == nil {
		cur = []announced{}
	}
	return s.write(ctx, struct {
		Type   string      `json:"type"`
		Agents []announced `json:"agents"`
	}{kind, cur})
}

func (s *session) live() []announced {
	list, err := s.store.List(time.Now())
	if err != nil {
		return s.announced
	}
	var out []announced
	for _, a := range list {
		if !a.Live {
			continue
		}
		// A remote-shaped name is an inbox link forwards, not a local agent.
		if _, _, ok := splitNode(a.Name); ok {
			continue
		}
		out = append(out, announced{Name: a.Name, Kind: a.Kind, AcceptExternal: a.AcceptExternal, Session: sessionOf(a.Agent)})
	}
	return out
}

func sameAgents(a, b []announced) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *session) ack(ctx context.Context, seq int64) error {
	return s.write(ctx, struct {
		Type string `json:"type"`
		Seq  int64  `json:"seq"`
	}{"ack", seq})
}

func (s *session) write(ctx context.Context, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, raw)
}

// readFor waits up to d for the next frame. The read itself is never given a
// deadline: the websocket library closes the connection when a read's context
// ends, so a poll timeout on the read dropped the link every poll interval.
// The read runs on its own and outlives the poll; the next call picks it up.
func (s *session) readFor(parent context.Context, d time.Duration) (incoming, error) {
	if !s.reading {
		s.reading = true
		go func() {
			b, err := s.conn.Read(s.readCtx)
			s.frames <- frame{b, err}
		}()
	}
	var timeout <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	var f frame
	select {
	case f = <-s.frames:
		s.reading = false
	case <-timeout:
		return incoming{}, errPoll
	case <-parent.Done():
		return incoming{}, parent.Err()
	}
	b, err := f.b, f.err
	if err != nil {
		return incoming{}, closeFatal(err)
	}
	var in incoming
	if err := json.Unmarshal(b, &in); err != nil {
		return incoming{}, errors.New("bad frame")
	}
	return in, nil
}

func closeFatal(err error) error {
	switch websocket.CloseStatus(err) {
	case closeReplaced:
		return errReplaced
	case closeRevoked:
		return errRevoked
	default:
		return err
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type wsConn struct{ c *websocket.Conn }

func (w wsConn) Read(ctx context.Context) ([]byte, error) {
	_, b, err := w.c.Read(ctx)
	return b, err
}

func (w wsConn) Write(ctx context.Context, p []byte) error {
	return w.c.Write(ctx, websocket.MessageText, p)
}

func (w wsConn) Close() error {
	return w.c.Close(websocket.StatusNormalClosure, "")
}

func defaultDial(ctx context.Context, rawURL, token string) (Conn, error) {
	if err := checkRelayBase(rawURL); err != nil {
		return nil, err
	}
	link, err := LinkURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errConfig, err)
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(dctx, link, &websocket.DialOptions{
		HTTPClient: &http.Client{CheckRedirect: refuseRedirect},
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	if err != nil {
		// A bad credential must not reconnect in a loop.
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return nil, errRevoked
		}
		if errors.Is(err, errRedirect) {
			return nil, fmt.Errorf("%w: redirect", errConfig)
		}
		return nil, err
	}
	// The library default is 32 KiB. Relay text from a machine is up to 64 KiB.
	c.SetReadLimit(128 << 10)
	return wsConn{c}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkRelayBase rejects a URL that would send a bearer off-box in cleartext.
// Loopback http stays open for a local test server.
func checkRelayBase(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return errConfig
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("%w: cleartext relay url", errConfig)
	default:
		return errConfig
	}
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return errRedirect
}

func sessionOf(a core.Agent) string {
	return fmt.Sprintf("%d-%s", a.PID, strings.NewReplacer(":", "", "+", "").Replace(a.Joined))
}
