//go:build unix

package core

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
)

// Observer helpers read inboxes without touching cursors, so watching the
// network never consumes or reorders anyone's messages.

// Inboxes lists every agent name that has an inbox file.
func (s Store) Inboxes() ([]string, error) {
	dir, err := s.dir("inbox")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".jsonl")
		if ok && ValidateName(name) == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func readShared(path string, fn func(*os.File) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn(f)
}

// Tail returns the whole messages written after offset and the offset to pass
// next time. An inbox shorter than offset was rotated, so it restarts at 0.
func (s Store) Tail(name string, offset int64) ([]Envelope, int64, error) {
	path, err := s.inboxPath(name)
	if err != nil {
		return nil, offset, err
	}
	var out []Envelope
	next := offset
	err = readShared(path, func(f *os.File) error {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if info.Size() < offset {
			offset = 0
		}
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
				env = Envelope{From: "?", To: name, Text: string(raw), Corrupt: true}
			}
			out = append(out, env)
		}
		next = offset + int64(end)
		return nil
	})
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	return out, next, err
}

// Unread counts whole messages past the agent's cursor.
func (s Store) Unread(name string) int {
	path, err := s.inboxPath(name)
	if err != nil {
		return 0
	}
	count := 0
	_ = readShared(path, func(f *os.File) error {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		offset := min(s.cursor(name), info.Size())
		data, err := io.ReadAll(io.NewSectionReader(f, offset, info.Size()-offset))
		if err != nil {
			return err
		}
		count = bytes.Count(data, []byte{'\n'})
		return nil
	})
	return count
}
