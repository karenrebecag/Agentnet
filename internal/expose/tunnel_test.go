package expose

import (
	"errors"
	"testing"
	"time"
)

func TestOpenDisabledDoesNotTouchTheBinary(t *testing.T) {
	_, err := Open(Options{
		LookPath: func(string) (string, error) {
			t.Fatal("looked up binary while disabled")
			return "", nil
		},
		Start: func(string) (Process, error) {
			t.Fatal("started while disabled")
			return nil, nil
		},
	})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("err %v", err)
	}
}

func TestOpenMissingBinary(t *testing.T) {
	_, err := Open(Options{
		Enabled: true,
		LookPath: func(string) (string, error) {
			return "", errors.New("absent")
		},
		Start: func(string) (Process, error) {
			t.Fatal("started without a binary")
			return nil, nil
		},
	})
	if !errors.Is(err, ErrBinaryMissing) {
		t.Fatalf("err %v", err)
	}
}

func TestOpenUsesInjectedStart(t *testing.T) {
	proc := &fakeProc{killed: make(chan struct{}), done: make(chan error)}
	sup, err := Open(Options{
		Enabled: true,
		TTL:     time.Hour,
		LookPath: func(string) (string, error) {
			return "cloudflared", nil
		},
		Start: func(string) (Process, error) {
			return proc, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sup.TTL != time.Hour {
		t.Fatalf("ttl %s", sup.TTL)
	}
}
