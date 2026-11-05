//go:build unix

package expose

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	quickURL   = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
	registered = regexp.MustCompile(`Registered tunnel connection`)
)

// StartQuickTunnel runs a Cloudflare quick tunnel to localURL and returns the
// public hostname once the edge has registered a connection: the URL is printed
// earlier, and requests before registration fail with Cloudflare error 1033.
// A quick tunnel gets a new hostname on every start, so the caller must end the
// session if it dies instead of restarting it. cloudflared's log goes to logPath.
func StartQuickTunnel(ctx context.Context, localURL, logPath, pidPath, protocol string) (Process, string, error) {
	bin, err := exec.LookPath("cloudflared")
	if err != nil {
		return nil, "", ErrBinaryMissing
	}
	KillOrphanTunnel(pidPath)
	if protocol == "" {
		protocol = "auto"
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, "", err
	}
	cmd := exec.Command(bin, "tunnel", "--no-autoupdate", "--protocol", protocol, "--url", localURL)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	proc := &cmdProc{cmd: cmd, done: make(chan error, 1)}
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	found := make(chan string, 1)
	go func() {
		defer logFile.Close()
		scanner := bufio.NewScanner(io.TeeReader(stderr, logFile))
		url, sent := "", false
		for scanner.Scan() {
			line := scanner.Text()
			if m := quickURL.FindString(line); m != "" && url == "" {
				url = m
			}
			if url != "" && !sent && registered.MatchString(line) {
				found <- url
				sent = true
			}
		}
	}()
	go func() { proc.done <- cmd.Wait() }()
	select {
	case u := <-found:
		return proc, u[len("https://"):], nil
	case err := <-proc.done:
		return nil, "", errors.Join(errors.New("cloudflared exited before registering; see "+logPath), err)
	case <-time.After(45 * time.Second):
		_ = proc.Kill()
		return nil, "", errors.New("cloudflared did not register a connection in 45s; see " + logPath)
	case <-ctx.Done():
		_ = proc.Kill()
		return nil, "", ctx.Err()
	}
}

// KillOrphanTunnel stops a cloudflared left running by an expose that died
// without cleanup (SIGKILL, panic, OOM). Such a tunnel would keep forwarding
// its public hostname to a port some other local server may now hold.
func KillOrphanTunnel(pidPath string) {
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return
	}
	defer os.Remove(pidPath)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return
	}
	// Only kill it if that pid is still cloudflared, not a reused pid.
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil || !strings.Contains(string(out), "cloudflared") {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
