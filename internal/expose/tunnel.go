package expose

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

var (
	// ErrDisabled is the default. A tunnel stays closed until an accepted
	// threat model flips Enabled.
	ErrDisabled = errors.New("expose is disabled")
	// ErrBinaryMissing means the tunnel program is not on PATH.
	// This module never downloads or installs it.
	ErrBinaryMissing = errors.New("cloudflared is not on PATH")
)

// Options configures a tunnel. Enabled defaults to false.
type Options struct {
	Enabled  bool
	TTL      time.Duration
	LookPath func(string) (string, error)
	Start    func(bin string) (Process, error)
}

// Open refuses to start unless Enabled is set and the binary is already installed.
// LookPath and Start are not called when the tunnel is disabled.
func Open(opt Options) (*Supervisor, error) {
	if !opt.Enabled {
		return nil, ErrDisabled
	}
	look := opt.LookPath
	if look == nil {
		look = exec.LookPath
	}
	bin, err := look("cloudflared")
	if err != nil {
		return nil, ErrBinaryMissing
	}
	start := opt.Start
	if start == nil {
		start = startProcess
	}
	return &Supervisor{
		TTL: opt.TTL,
		Start: func(context.Context) (Process, error) {
			return start(bin)
		},
	}, nil
}
