//go:build unix

package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// A joined agent touches its registration every poll; older than this means it
// died, or its pid now belongs to an unrelated process.
const StaleAfter = 5 * time.Second

var ErrNameTaken = errors.New("name is live in another process")

type Agent struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	PID    int    `json:"pid"`
	CWD    string `json:"cwd,omitempty"`
	Joined string `json:"joined"`
	// AcceptExternal opts an agent into messages from exposed guests. Off by
	// default: an agent that did not ask for it is invisible to guests.
	AcceptExternal bool `json:"accept_external,omitempty"`
}

type AgentStatus struct {
	Agent
	Live bool
}

func (s Store) agentPath(name string) (string, error) {
	dir, err := s.dir("agents")
	return filepath.Join(dir, name+".json"), err
}

func (s Store) readAgent(name string) (Agent, os.FileInfo, error) {
	path, err := s.agentPath(name)
	if err != nil {
		return Agent{}, nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Agent{}, nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Agent{}, nil, err
	}
	var agent Agent
	if err := json.Unmarshal(raw, &agent); err != nil {
		return Agent{}, nil, err
	}
	return agent, info, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (s Store) IsLive(name string, now time.Time) bool {
	agent, info, err := s.readAgent(name)
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) <= StaleAfter && processAlive(agent.PID)
}

// Register claims a name for this process. The registry lock makes the
// check-then-write atomic, so two joins racing for one name cannot both win.
func (s Store) Register(name, kind string, acceptExternal bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	dir, err := s.dir("agents")
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	return withLock(lock, func() error {
		if current, _, err := s.readAgent(name); err == nil && current.PID != os.Getpid() && s.IsLive(name, time.Now()) {
			return fmt.Errorf("%w: %s", ErrNameTaken, name)
		}
		cwd, _ := os.Getwd()
		data, err := json.Marshal(Agent{Name: name, Kind: kind, PID: os.Getpid(), CWD: cwd,
			Joined: time.Now().UTC().Format(time.RFC3339), AcceptExternal: acceptExternal})
		if err != nil {
			return err
		}
		path, err := s.agentPath(name)
		if err != nil {
			return err
		}
		return writeAtomic(path, data)
	})
}

// OpenToGuests lets guests reach the session that holds name right now. The
// flag lives in that session's registration, so it ends with the session: the
// next process to join under the same name starts closed again.
func (s Store) OpenToGuests(name string) error {
	dir, err := s.dir("agents")
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	return withLock(lock, func() error {
		agent, _, err := s.readAgent(name)
		if err != nil || !s.IsLive(name, time.Now()) {
			return fmt.Errorf("%s is not in the network right now", name)
		}
		if agent.AcceptExternal {
			return nil
		}
		agent.AcceptExternal = true
		data, err := json.Marshal(agent)
		if err != nil {
			return err
		}
		path, err := s.agentPath(name)
		if err != nil {
			return err
		}
		return writeAtomic(path, data)
	})
}

func (s Store) Heartbeat(name string) error {
	path, err := s.agentPath(name)
	if err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// Unregister only drops a registration this process owns.
func (s Store) Unregister(name string) error {
	agent, _, err := s.readAgent(name)
	if err != nil || agent.PID != os.Getpid() {
		return nil
	}
	return s.Leave(name)
}

func (s Store) Leave(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	path, err := s.agentPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s Store) Lookup(name string, now time.Time) (AgentStatus, bool) {
	agent, _, err := s.readAgent(name)
	if err != nil {
		return AgentStatus{}, false
	}
	return AgentStatus{Agent: agent, Live: s.IsLive(name, now)}, true
}

func (s Store) List(now time.Time) ([]AgentStatus, error) {
	dir, err := s.dir("agents")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []AgentStatus
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || ValidateName(name) != nil {
			continue
		}
		agent, _, err := s.readAgent(name)
		if err != nil {
			continue
		}
		out = append(out, AgentStatus{Agent: agent, Live: s.IsLive(name, now)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
