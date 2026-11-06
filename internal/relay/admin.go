//go:build unix

package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// errPairHost is a hostname check, not a relay response. IP addresses and URLs
// are rejected so a pairing code cannot be bound to a redirect trick.
var errPairHost = errors.New("pair host must be a hostname")

var signinRE = regexp.MustCompile(`^[0-9a-f]{8}$`)

// Node is one row of GET /v1/admin/nodes. The relay never returns token hashes.
type Node struct {
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	Targets  []string    `json:"targets,omitempty"`
	Revoked  bool        `json:"revoked"`
	Created  string      `json:"created,omitempty"`
	LastSeen string      `json:"last_seen,omitempty"`
	Agents   []NodeAgent `json:"agents,omitempty"`
}

type NodeAgent struct {
	Name           string `json:"name"`
	Kind           string `json:"kind,omitempty"`
	AcceptExternal bool   `json:"accept_external,omitempty"`
}

// Client calls /v1/admin with the admin token. link does not construct one.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		if c.HTTP.CheckRedirect != nil {
			return c.HTTP
		}
		clone := *c.HTTP
		clone.CheckRedirect = refuseRedirect
		return &clone
	}
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: refuseRedirect}
}

func (c Client) Add(ctx context.Context, name, kind string, targets []string, inbox bool) (string, error) {
	body := map[string]any{"name": name, "kind": kind}
	if len(targets) > 0 {
		body["targets"] = targets
	}
	// false is the relay default. Omitting it keeps an older worker from seeing a new field.
	if inbox {
		body["inbox"] = true
	}
	var out struct {
		Token string `json:"token"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/admin/nodes", body, &out)
	return out.Token, err
}

func (c Client) List(ctx context.Context) ([]Node, error) {
	var out []Node
	err := c.do(ctx, http.MethodGet, "/v1/admin/nodes", nil, &out)
	return out, err
}

func (c Client) Rotate(ctx context.Context, name string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/admin/nodes/"+url.PathEscape(name)+"/rotate", map[string]any{}, &out)
	return out.Token, err
}

func (c Client) Revoke(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/v1/admin/nodes/"+url.PathEscape(name)+"/revoke", map[string]any{}, nil)
}

func (c Client) do(ctx context.Context, method, path string, body, out any) error {
	if c.Token == "" {
		return errors.New("missing admin token")
	}
	if err := checkRelayBase(c.BaseURL); err != nil {
		return err
	}
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(raw)
	}
	base := strings.TrimRight(c.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		if errors.Is(err, errRedirect) {
			return fmt.Errorf("%w: redirect", errConfig)
		}
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay admin %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func adminClient(home string) (Client, File, error) {
	f, err := Load(home)
	if err != nil {
		return Client{}, File{}, err
	}
	if f.URL == "" {
		return Client{}, File{}, errors.New("relay url missing (relay init --url)")
	}
	admin, err := loadAdmin(home)
	if err != nil {
		return Client{}, File{}, errors.New("admin token missing (relay admin-token)")
	}
	return Client{BaseURL: f.URL, Token: admin.Token}, f, nil
}

func checkNode(name, kind string, targets []string, inbox bool) error {
	if !nodeRE.MatchString(name) {
		return fmt.Errorf("%w: %q", errBadAddress, name)
	}
	switch kind {
	case "machine":
		if len(targets) > 0 {
			return errors.New("a machine node does not take --targets")
		}
		if inbox {
			return errors.New("a machine node does not take --inbox")
		}
	case "guest":
		if len(targets) == 0 {
			return errors.New("a guest needs --targets")
		}
		for _, tgt := range targets {
			if _, _, err := ParseAddress(tgt); err != nil {
				return err
			}
		}
	default:
		return errors.New("kind must be machine or guest")
	}
	return nil
}

// AddNode creates a node with inbox left off. When this machine has no node yet,
// the token is saved into relay.json. A second machine must not clobber that file.
func AddNode(ctx context.Context, home, name, kind string, targets []string) (token string, saved bool, err error) {
	return addNode(ctx, home, name, kind, targets, false)
}

// AddNodeOpts is AddNode with an explicit inbox opt-in. Only a guest may set it.
func AddNodeOpts(ctx context.Context, home, name, kind string, targets []string, inbox bool) (token string, saved bool, err error) {
	return addNode(ctx, home, name, kind, targets, inbox)
}

func addNode(ctx context.Context, home, name, kind string, targets []string, inbox bool) (token string, saved bool, err error) {
	if err := checkNode(name, kind, targets, inbox); err != nil {
		return "", false, err
	}
	c, f, err := adminClient(home)
	if err != nil {
		return "", false, err
	}
	token, err = c.Add(ctx, name, kind, targets, inbox)
	if err != nil {
		return "", false, err
	}
	if f.Node != "" {
		return token, false, nil
	}
	f.Node = name
	f.Token = token
	if err := Save(home, f); err != nil {
		return token, false, err
	}
	return token, true, nil
}

func ListNodes(ctx context.Context, home string) ([]Node, error) {
	c, _, err := adminClient(home)
	if err != nil {
		return nil, err
	}
	return c.List(ctx)
}

// RotateNode returns the new token. Rotating this machine's node updates relay.json.
func RotateNode(ctx context.Context, home, name string) (string, error) {
	if !nodeRE.MatchString(name) {
		return "", fmt.Errorf("%w: %q", errBadAddress, name)
	}
	c, f, err := adminClient(home)
	if err != nil {
		return "", err
	}
	token, err := c.Rotate(ctx, name)
	if err != nil {
		return "", err
	}
	if f.Node == name {
		f.Token = token
		if err := Save(home, f); err != nil {
			return "", err
		}
	}
	return token, nil
}

// RevokeNode disables a node. Revoking this machine clears the token it would dial with.
func RevokeNode(ctx context.Context, home, name string) error {
	if !nodeRE.MatchString(name) {
		return fmt.Errorf("%w: %q", errBadAddress, name)
	}
	c, f, err := adminClient(home)
	if err != nil {
		return err
	}
	if err := c.Revoke(ctx, name); err != nil {
		return err
	}
	if f.Node == name {
		f.Token = ""
		return Save(home, f)
	}
	return nil
}

// Pairing is a one-time code bound to a redirect host. Callers print it; nothing here stores it.
type Pairing struct {
	Code         string
	Expires      string
	ConnectorURL string
}

// PairNode asks the admin API for a pairing code. host is a hostname, not a
// URL; "" leaves the code unbound, and the first client to use it waits for
// ApproveSignin. The code is not written to disk.
func PairNode(ctx context.Context, home, name, host string) (Pairing, error) {
	body := map[string]string{}
	if strings.TrimSpace(host) != "" {
		h, err := normalizePairHost(host)
		if err != nil {
			return Pairing{}, err
		}
		body["for"] = h
	}
	if !nodeRE.MatchString(name) {
		return Pairing{}, fmt.Errorf("%w: %q", errBadAddress, name)
	}
	c, _, err := adminClient(home)
	if err != nil {
		return Pairing{}, err
	}
	var out struct {
		Code         string `json:"code"`
		Expires      string `json:"expires"`
		ConnectorURL string `json:"connector_url"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/admin/nodes/"+url.PathEscape(name)+"/pair", body, &out); err != nil {
		return Pairing{}, err
	}
	if !acceptableCode(out.Code) {
		return Pairing{}, errors.New("relay pair returned no code")
	}
	return presentPairing(Pairing{Code: out.Code, Expires: out.Expires, ConnectorURL: out.ConnectorURL}), nil
}

func normalizePairHost(raw string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 || strings.Contains(host, "..") {
		return "", errPairHost
	}
	if strings.ContainsAny(host, "/:@?#\\*[] \t\r\n") {
		return "", errPairHost
	}
	if net.ParseIP(host) != nil || numericPairHost(host) {
		return "", errPairHost
	}
	for _, label := range strings.Split(host, ".") {
		if !pairLabelOK(label) {
			return "", errPairHost
		}
	}
	return host, nil
}

func numericPairHost(host string) bool {
	if strings.HasPrefix(host, "0x") {
		return true
	}
	labels := strings.Split(host, ".")
	if len(labels) == 1 {
		return allDigits(labels[0])
	}
	if len(labels) != 4 {
		return false
	}
	for _, label := range labels {
		if !allDigits(label) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func pairLabelOK(label string) bool {
	if label == "" || len(label) > 63 {
		return false
	}
	for i, c := range label {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0 && i < len(label)-1:
		default:
			return false
		}
	}
	return true
}

func acceptableCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, r := range code {
		if r <= ' ' || r >= 0x7f {
			return false
		}
	}
	return true
}

func presentPairing(p Pairing) Pairing {
	u, err := url.Parse(p.ConnectorURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(p.ConnectorURL, p.Code) {
		p.ConnectorURL = ""
	}
	if _, err := time.Parse(time.RFC3339, p.Expires); err != nil || strings.Contains(p.Expires, p.Code) {
		p.Expires = ""
	}
	return p
}

// Signin is a client that used an unbound code and waits for a yes. Host is
// where the client returns: it is the one field worth reading before approving.
type Signin struct {
	ID         string `json:"id"`
	Guest      string `json:"guest"`
	Client     string `json:"client"`
	ClientName string `json:"client_name"`
	Host       string `json:"host"`
	IP         string `json:"ip"`
	Created    string `json:"created"`
	Expires    string `json:"expires"`
}

func ListSignins(ctx context.Context, home string) ([]Signin, error) {
	c, _, err := adminClient(home)
	if err != nil {
		return nil, err
	}
	var out []Signin
	err = c.do(ctx, http.MethodGet, "/v1/admin/signins", nil, &out)
	return out, err
}

// DecideSignin approves or denies a waiting sign-in.
func DecideSignin(ctx context.Context, home, id string, approve bool) error {
	if !signinRE.MatchString(id) {
		return fmt.Errorf("sign-in id must be 8 hex characters, got %q", id)
	}
	c, _, err := adminClient(home)
	if err != nil {
		return err
	}
	verb := "deny"
	if approve {
		verb = "approve"
	}
	return c.do(ctx, http.MethodPost, "/v1/admin/signins/"+id+"/"+verb, map[string]any{}, nil)
}
