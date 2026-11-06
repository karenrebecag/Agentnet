//go:build unix

package relay

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/karenrebecag/agentnet/internal/core"
)

const OriginRelay = "relay"

var (
	errBadAddress = errors.New("invalid address")
	errReserved   = errors.New("reserved node")

	// A node has no dots, so an address always splits at the first one.
	nodeRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// ParseAddress splits "<node>.<agent>" at the first dot. The agent part may
// contain dots. "ext" and "local" are reserved because expose already uses them.
func ParseAddress(addr string) (node, agent string, err error) {
	node, agent, ok := splitNode(addr)
	if !ok {
		return "", "", errBadAddress
	}
	if node == "ext" || node == "local" {
		return "", "", errReserved
	}
	return node, agent, nil
}

func splitNode(name string) (node, agent string, ok bool) {
	node, agent, ok = strings.Cut(name, ".")
	if !ok || node == "" || agent == "" || !nodeRE.MatchString(node) || core.ValidateName(agent) != nil {
		return "", "", false
	}
	return node, agent, true
}

// LinkURL maps the https URL stored on disk to the websocket endpoint.
// http is accepted so tests can use httptest. Credentials and queries are rejected
// so a token cannot travel in the URL.
func LinkURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return "", errors.New("bad relay url")
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", errors.New("relay url must be http or https")
	}
	u.Path = "/v1/link"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
