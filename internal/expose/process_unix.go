//go:build unix

package expose

import (
	"os/exec"
	"syscall"
)

type cmdProc struct {
	cmd  *exec.Cmd
	done chan error
}

// startProcess puts the child in its own group so Kill reaches the tunnel
// and anything it spawned.
func startProcess(bin string) (Process, error) {
	return startCommand(bin)
}

func startCommand(bin string, args ...string) (Process, error) {
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &cmdProc{cmd: cmd, done: make(chan error, 1)}
	go func() {
		p.done <- cmd.Wait()
	}()
	return p, nil
}

func (p *cmdProc) Done() <-chan error { return p.done }

func (p *cmdProc) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	// The group id is the child pid because Setpgid was set before start.
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	return p.cmd.Process.Kill()
}
