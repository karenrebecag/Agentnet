//go:build unix

package expose

import (
	"testing"
	"time"
)

func TestKillStopsTheProcessGroup(t *testing.T) {
	proc, err := startCommand("/bin/sleep", "30")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Kill() })
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proc.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("process still running")
	}
}
