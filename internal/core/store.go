//go:build unix

// Package core is the on-disk message bus: one JSONL inbox per agent, a read
// cursor, and a registry whose file mtime is the agent's heartbeat.
package core

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// Only fully read inboxes are truncated, so no unread message is lost.
	RotateBytes = 1 << 20
	// A single message is bounded so one sender cannot fill the disk or a
	// reader's context in one write.
	MaxTextBytes = 256 << 10
)

var (
	ErrInvalidName = errors.New("invalid agent name")
	ErrEmptyText   = errors.New("empty message")
	ErrTooLarge    = fmt.Errorf("message larger than %d bytes", MaxTextBytes)

	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
)

// Envelope is the wire format shared with the v0 Python prototype.
type Envelope struct {
	ID   string `json:"id"`
	TS   string `json:"ts"`
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
	// Origin is "external" for messages that entered through expose. Readers
	// must treat those as third-party data, never as their user's instruction.
	Origin  string `json:"origin,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"`
	Corrupt bool   `json:"corrupt,omitempty"`
}

const OriginExternal = "external"

// SendOptions carries the optional envelope fields.
type SendOptions struct {
	Origin  string
	ReplyTo string
}

type Store struct {
	Home string
}

func DefaultHome() (string, error) {
	if home := os.Getenv("AGENTNET_HOME"); home != "" {
		return home, nil
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(user, ".agentnet"), nil
}

func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}

func (s Store) dir(sub string) (string, error) {
	path := filepath.Join(s.Home, sub)
	return path, os.MkdirAll(path, 0o700)
}

func (s Store) inboxPath(name string) (string, error) {
	dir, err := s.dir("inbox")
	return filepath.Join(dir, name+".jsonl"), err
}

func (s Store) cursorPath(name string) (string, error) {
	dir, err := s.dir("inbox")
	return filepath.Join(dir, name+".cursor"), err
}

// NewID is 128 random bits: ids double as reply handles, so they must not be guessable.
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func withLock(f *os.File, fn func() error) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// Send appends one envelope under an exclusive flock, so concurrent senders
// never interleave bytes of a long line.
func (s Store) Send(to, from, text string) (Envelope, error) {
	return s.SendWith(to, from, text, SendOptions{})
}

func (s Store) SendWith(to, from, text string, opts SendOptions) (Envelope, error) {
	if err := ValidateName(to); err != nil {
		return Envelope{}, err
	}
	if err := ValidateName(from); err != nil {
		return Envelope{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Envelope{}, ErrEmptyText
	}
	if len(text) > MaxTextBytes {
		return Envelope{}, ErrTooLarge
	}
	id, err := NewID()
	if err != nil {
		return Envelope{}, err
	}
	env := Envelope{ID: id, TS: time.Now().UTC().Format(time.RFC3339Nano), From: from, To: to, Text: text,
		Origin: opts.Origin, ReplyTo: opts.ReplyTo}
	line, err := json.Marshal(env)
	if err != nil {
		return Envelope{}, err
	}
	path, err := s.inboxPath(to)
	if err != nil {
		return Envelope{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return Envelope{}, err
	}
	defer f.Close()
	err = withLock(f, func() error {
		_, werr := f.Write(append(line, '\n'))
		return werr
	})
	return env, err
}

// Batch is a set of unread messages plus the offset that marks them read.
type Batch struct {
	Messages []Envelope
	next     int64
}

// Peek returns unread whole lines without consuming them. A line still being
// written has no trailing newline and stays unread. Callers Commit after the
// messages reached the agent, so a crash between both re-delivers instead of losing.
func (s Store) Peek(name string) (Batch, error) {
	path, err := s.inboxPath(name)
	if err != nil {
		return Batch{}, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return Batch{}, err
	}
	defer f.Close()
	var batch Batch
	err = withLock(f, func() error {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		offset := min(s.cursor(name), info.Size())
		data, err := io.ReadAll(io.NewSectionReader(f, offset, info.Size()-offset))
		if err != nil {
			return err
		}
		end := bytes.LastIndexByte(data, '\n') + 1
		for _, raw := range bytes.Split(data[:end], []byte{'\n'}) {
			if len(bytes.TrimSpace(raw)) == 0 {
				continue
			}
			var env Envelope
			if json.Unmarshal(raw, &env) != nil {
				env = Envelope{From: "?", Text: string(raw), Corrupt: true}
			}
			batch.Messages = append(batch.Messages, env)
		}
		batch.next = offset + int64(end)
		return nil
	})
	return batch, err
}

// Commit marks a batch as read and truncates the inbox once it is past the
// rotation size and fully consumed.
func (s Store) Commit(name string, batch Batch) error {
	path, err := s.inboxPath(name)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return withLock(f, func() error {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		offset := batch.next
		if offset >= RotateBytes && offset == info.Size() {
			if err := f.Truncate(0); err != nil {
				return err
			}
			offset = 0
		}
		return s.setCursor(name, offset)
	})
}

func (s Store) cursor(name string) int64 {
	path, err := s.cursorPath(name)
	if err != nil {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (s Store) setCursor(name string, offset int64) error {
	path, err := s.cursorPath(name)
	if err != nil {
		return err
	}
	return writeAtomic(path, []byte(strconv.FormatInt(offset, 10)))
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
