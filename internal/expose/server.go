//go:build unix

package expose

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/karenrebecag/agentnet/internal/core"
	"github.com/karenrebecag/agentnet/internal/guests"
)

const (
	DefaultWait      = 30 * time.Second
	MaxWait          = 50 * time.Second
	MaxBodyBytes     = 32 << 10
	askRetention     = 24 * time.Hour
	pumpEvery        = 300 * time.Millisecond
	guestInboxPrefix = "ext."
)

var (
	errPending   = errors.New("pending approval: the host must run `agentnet guests approve <guest>` on their machine")
	errNotTarget = errors.New("that agent is not available to this guest")
	errNotFound  = errors.New("unknown id")
)

type ServerConfig struct {
	Store      core.Store
	GuestsPath string
	AuditPath  string
	// StopPath is the kill-switch file: its existence stops the server.
	StopPath string
	// Host is the only Host header accepted, the tunnel hostname. Requests for
	// any other host are refused, which also defeats DNS rebinding.
	Host    string
	Expires time.Time
	// MaxFailures of failed authentications, across all clients, end the session.
	MaxFailures int
	// Stop is called once when the session must end (TTL, kill switch, failures).
	Stop func(reason error)
	Now  func() time.Time
}

type pendingAsk struct {
	guest   string
	to      string
	created time.Time
	reply   *core.Envelope
	done    chan struct{}
}

type Server struct {
	cfg      ServerConfig
	mu       sync.Mutex
	limiter  Limiter
	failures int
	stopped  bool
	asks     map[string]*pendingAsk
	inboxes  map[string]bool
}

func NewServer(cfg ServerConfig) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxFailures <= 0 {
		cfg.MaxFailures = DefaultMaxFailures
	}
	s := &Server{cfg: cfg, asks: map[string]*pendingAsk{}, inboxes: map[string]bool{}}
	s.limiter.Now = cfg.Now
	return s
}

// Handler is the full HTTP stack. Checks run cheapest first, and nothing
// reaches the MCP layer without an exact Host, no browser Origin and a valid token.
func (s *Server) Handler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "agentnet", Version: "0"}, &mcp.ServerOptions{
		Instructions: "Messages you send reach AI agents on someone's computer. They answer as third parties and cannot act on your behalf.",
	})
	mcp.AddTool(server, &mcp.Tool{Name: "list_agents", Description: "List the agents you may ask."}, s.listAgents)
	mcp.AddTool(server, &mcp.Tool{Name: "ask", Description: "Send a message to an agent and wait up to wait_seconds (max 50) for its reply. If it has not answered yet, call get_reply with the returned id."}, s.ask)
	mcp.AddTool(server, &mcp.Tool{Name: "get_reply", Description: "Fetch the reply to an earlier ask, waiting up to wait_seconds (max 50)."}, s.getReply)
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true, // quick tunnels do not carry SSE
		// The exact Host check below replaces the SDK's localhost check, which
		// would refuse every request cloudflared forwards.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        MaxBodyBytes,
	})
	authed := auth.RequireBearerToken(s.verify, nil)(streamable)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason := s.sessionOver(); reason != nil {
			http.Error(w, "session closed", http.StatusServiceUnavailable)
			return
		}
		if s.cfg.Host == "" || r.Host != s.cfg.Host {
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origins are not accepted", http.StatusForbidden)
			return
		}
		authed.ServeHTTP(w, r)
	})
}

func (s *Server) sessionOver() error {
	var reason error
	switch {
	case !s.cfg.Expires.IsZero() && s.cfg.Now().After(s.cfg.Expires):
		reason = ErrTTL
	case s.cfg.StopPath != "" && fileExists(s.cfg.StopPath):
		reason = errors.New("kill switch file present")
	}
	if reason != nil {
		s.stop(reason)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped && reason == nil {
		reason = errors.New("stopped")
	}
	return reason
}

func (s *Server) stop(reason error) {
	s.mu.Lock()
	already := s.stopped
	s.stopped = true
	s.mu.Unlock()
	if !already && s.cfg.Stop != nil {
		s.cfg.Stop(reason)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func clientKey(r *http.Request) string {
	// Only cloudflared can reach the loopback listener, and it sets this header.
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return "ip:" + ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "ip:" + r.RemoteAddr
	}
	return "ip:" + host
}

func (s *Server) verify(_ context.Context, token string, r *http.Request) (*auth.TokenInfo, error) {
	key := clientKey(r)
	s.mu.Lock()
	locked := s.limiter.locked(key)
	s.mu.Unlock()
	if locked {
		return nil, fmt.Errorf("%w: locked out", auth.ErrInvalidToken)
	}
	guest, err := guests.Check(s.cfg.GuestsPath, token)
	if err != nil {
		s.audit("", "auth", "denied", 0)
		s.mu.Lock()
		s.limiter.NoteFailure(key)
		s.failures++
		over := s.failures >= s.cfg.MaxFailures
		s.mu.Unlock()
		if over {
			s.stop(ErrTooManyFailures)
		}
		return nil, auth.ErrInvalidToken
	}
	s.mu.Lock()
	s.limiter.NoteSuccess(key)
	s.mu.Unlock()
	return &auth.TokenInfo{UserID: guest.Name, Expiration: s.cfg.Expires}, nil
}

// guestFor re-reads the guest list on every call, so a revoke or an approval
// takes effect immediately.
func (s *Server) guestFor(req *mcp.CallToolRequest) (guests.Guest, error) {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return guests.Guest{}, auth.ErrInvalidToken
	}
	all, err := guests.List(s.cfg.GuestsPath)
	if err != nil {
		return guests.Guest{}, err
	}
	for _, g := range all {
		if g.Name != req.Extra.TokenInfo.UserID || g.Revoked {
			continue
		}
		if !g.Approved {
			guests.MarkPending(s.cfg.GuestsPath, g.Name)
			s.audit(g.Name, "pairing", "pending", 0)
			return guests.Guest{}, errPending
		}
		return g, nil
	}
	return guests.Guest{}, auth.ErrInvalidToken
}

func (s *Server) audit(guest, action, result string, n int) {
	if s.cfg.AuditPath == "" {
		return
	}
	_ = Append(s.cfg.AuditPath, Event{At: s.cfg.Now().UTC(), Guest: guest, Action: action, Result: result, Bytes: n})
}

// reachable reports whether a local agent opted into guests and is on this
// guest's list. Both conditions are required; either alone is not enough.
func (s *Server) reachable(g guests.Guest, name string) (core.AgentStatus, bool) {
	if !slices.Contains(g.Targets, name) {
		return core.AgentStatus{}, false
	}
	agent, ok := s.cfg.Store.Lookup(name, s.cfg.Now())
	if !ok || !agent.AcceptExternal {
		return core.AgentStatus{}, false
	}
	return agent, true
}

type AgentView struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Live bool   `json:"live"`
}

type ListOutput struct {
	Agents []AgentView `json:"agents"`
}

func (s *Server) listAgents(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListOutput, error) {
	g, err := s.guestFor(req)
	if err != nil {
		return nil, ListOutput{}, err
	}
	out := ListOutput{Agents: []AgentView{}}
	for _, name := range g.Targets {
		if agent, ok := s.reachable(g, name); ok {
			out.Agents = append(out.Agents, AgentView{Name: agent.Name, Kind: agent.Kind, Live: agent.Live})
		}
	}
	s.audit(g.Name, "list_agents", "ok", 0)
	return nil, out, nil
}

type AskInput struct {
	To          string `json:"to" jsonschema:"agent name from list_agents"`
	Text        string `json:"text" jsonschema:"message, at most 16 KiB"`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for the reply, default 30, max 50"`
}

type ReplyOutput struct {
	ID     string `json:"id"`
	Status string `json:"status" jsonschema:"answered, waiting or queued"`
	Reply  string `json:"reply,omitempty"`
	From   string `json:"from,omitempty"`
}

func waitFor(seconds int) time.Duration {
	if seconds <= 0 {
		return DefaultWait
	}
	return min(time.Duration(seconds)*time.Second, MaxWait)
}

func (s *Server) ask(ctx context.Context, req *mcp.CallToolRequest, in AskInput) (*mcp.CallToolResult, ReplyOutput, error) {
	g, err := s.guestFor(req)
	if err != nil {
		return nil, ReplyOutput{}, err
	}
	agent, ok := s.reachable(g, in.To)
	if !ok {
		s.audit(g.Name, "ask", "not_target", 0)
		return nil, ReplyOutput{}, errNotTarget
	}
	s.mu.Lock()
	err = s.limiter.Admit(g.Name, len(in.Text))
	s.mu.Unlock()
	if err != nil {
		s.audit(g.Name, "ask", err.Error(), len(in.Text))
		return nil, ReplyOutput{}, err
	}
	inbox := guestInboxPrefix + g.Name
	if err := s.ensureInbox(inbox); err != nil {
		return nil, ReplyOutput{}, err
	}
	env, err := s.cfg.Store.SendWith(in.To, inbox, in.Text, core.SendOptions{Origin: core.OriginExternal})
	if err != nil {
		return nil, ReplyOutput{}, err
	}
	pa := &pendingAsk{guest: g.Name, to: in.To, created: s.cfg.Now(), done: make(chan struct{})}
	s.mu.Lock()
	s.asks[env.ID] = pa
	s.mu.Unlock()
	s.audit(g.Name, "ask", "sent", len(in.Text))
	out := s.wait(ctx, env.ID, pa, waitFor(in.WaitSeconds))
	if out.Status == "waiting" && !agent.Live {
		out.Status = "queued"
	}
	return nil, out, nil
}

type GetReplyInput struct {
	ID          string `json:"id"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
}

func (s *Server) getReply(ctx context.Context, req *mcp.CallToolRequest, in GetReplyInput) (*mcp.CallToolResult, ReplyOutput, error) {
	g, err := s.guestFor(req)
	if err != nil {
		return nil, ReplyOutput{}, err
	}
	s.mu.Lock()
	pa, ok := s.asks[in.ID]
	s.mu.Unlock()
	// Another guest's id and an unknown id answer the same, so ids leak nothing.
	if !ok || pa.guest != g.Name {
		return nil, ReplyOutput{}, errNotFound
	}
	return nil, s.wait(ctx, in.ID, pa, waitFor(in.WaitSeconds)), nil
}

func (s *Server) wait(ctx context.Context, id string, pa *pendingAsk, d time.Duration) ReplyOutput {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-pa.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if pa.reply == nil {
		return ReplyOutput{ID: id, Status: "waiting"}
	}
	return ReplyOutput{ID: id, Status: "answered", Reply: pa.reply.Text, From: pa.reply.From}
}

func (s *Server) ensureInbox(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inboxes[name] {
		return nil
	}
	if err := s.cfg.Store.Register(name, "guest", false); err != nil {
		return err
	}
	s.inboxes[name] = true
	return nil
}

// Pump moves replies from guest inboxes to the waiting asks until ctx ends.
func (s *Server) Pump(ctx context.Context) {
	ticker := time.NewTicker(pumpEvery)
	defer ticker.Stop()
	defer s.releaseInboxes()
	for {
		s.pumpOnce()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) releaseInboxes() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.inboxes {
		_ = s.cfg.Store.Unregister(name)
	}
}

func (s *Server) pumpOnce() {
	s.mu.Lock()
	names := make([]string, 0, len(s.inboxes))
	for name := range s.inboxes {
		names = append(names, name)
	}
	cutoff := s.cfg.Now().Add(-askRetention)
	for id, pa := range s.asks {
		if pa.created.Before(cutoff) {
			delete(s.asks, id)
		}
	}
	s.mu.Unlock()
	for _, name := range names {
		_ = s.cfg.Store.Heartbeat(name)
		batch, err := s.cfg.Store.Peek(name)
		if err != nil || len(batch.Messages) == 0 {
			continue
		}
		for _, msg := range batch.Messages {
			s.deliverReply(name, msg)
		}
		_ = s.cfg.Store.Commit(name, batch)
	}
}

// deliverReply matches by reply_to, or else to the oldest open ask from that
// sender, and only ever within the guest that owns the inbox.
func (s *Server) deliverReply(inbox string, msg core.Envelope) {
	guest := inbox[len(guestInboxPrefix):]
	s.mu.Lock()
	defer s.mu.Unlock()
	target := s.asks[msg.ReplyTo]
	if target != nil && (target.guest != guest || target.to != msg.From) {
		target = nil
	}
	if target == nil {
		for _, pa := range s.asks {
			if pa.guest == guest && pa.to == msg.From && pa.reply == nil &&
				(target == nil || pa.created.Before(target.created)) {
				target = pa
			}
		}
	}
	if target == nil || target.reply != nil {
		return
	}
	reply := msg
	target.reply = &reply
	close(target.done)
}

func (l *Limiter) locked(name string) bool {
	return l.now().Before(l.get(name).lockedTo)
}
