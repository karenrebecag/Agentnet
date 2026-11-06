//go:build unix

package relay

import (
	"context"
	"errors"
	"net/url"
	"testing"
)

func TestParseAddress(t *testing.T) {
	ok := []struct{ in, node, agent string }{
		{"laptop.claude", "laptop", "claude"},
		{"laptop.claude.api", "laptop", "claude.api"},
		{"a.b", "a", "b"},
	}
	for _, tc := range ok {
		node, agent, err := ParseAddress(tc.in)
		if err != nil || node != tc.node || agent != tc.agent {
			t.Fatalf("%s -> %s %s %v", tc.in, node, agent, err)
		}
	}
	for _, in := range []string{"claude", "Laptop.claude", "laptop.", ".claude", "a/b.c", ""} {
		if _, _, err := ParseAddress(in); !errors.Is(err, errBadAddress) {
			t.Fatalf("%q err = %v", in, err)
		}
	}
	for _, in := range []string{"ext.guest", "local.claude", "admin.claude"} {
		if _, _, err := ParseAddress(in); !errors.Is(err, errReserved) {
			t.Fatalf("%q err = %v", in, err)
		}
	}
}

func TestLinkURL(t *testing.T) {
	got, err := LinkURL("https://example.test/ignored?x=1")
	if err != nil || got != "wss://example.test/v1/link" {
		t.Fatalf("%s %v", got, err)
	}
	got, err = LinkURL("http://127.0.0.1:9")
	if err != nil || got != "ws://127.0.0.1:9/v1/link" {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := LinkURL("ftp://example.test"); err == nil {
		t.Fatal("ftp accepted")
	}
	withUser := (&url.URL{Scheme: "https", Host: "example.test", User: url.UserPassword("user", "secret")}).String()
	if _, err := LinkURL(withUser); err == nil {
		t.Fatal("credentials accepted")
	}
}

func TestCleartextRelayURLIsRejected(t *testing.T) {
	if _, err := defaultDial(context.Background(), "http://example.test", "an_secret"); !errors.Is(err, errConfig) {
		t.Fatalf("err = %v", err)
	}
}
