// Package guests holds the local guest list. Approval never happens through a tunnel.
package guests

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"

	"github.com/karenrebecag/agentnet/internal/filelock"
)

var (
	ErrDenied   = errors.New("guest denied")
	ErrExists   = errors.New("guest already exists")
	ErrNotFound = errors.New("guest not found")
	ErrName     = errors.New("invalid guest name")
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Guest is the public view. The token and its hash are not in this struct.
type Guest struct {
	Name     string   `json:"name"`
	Revoked  bool     `json:"revoked"`
	Approved bool     `json:"approved"`
	Pending  bool     `json:"pending"`
	Targets  []string `json:"targets"`
}

type record struct {
	Name     string   `json:"name"`
	Hash     string   `json:"hash"`
	Revoked  bool     `json:"revoked"`
	Approved bool     `json:"approved"`
	Pending  bool     `json:"pending"`
	Targets  []string `json:"targets"`
}

func (r record) public() Guest {
	return Guest{Name: r.Name, Revoked: r.Revoked, Approved: r.Approved, Pending: r.Pending, Targets: r.Targets}
}

// Add creates a guest and returns the token once. The file stores only the hash.
// Targets are the local agents this guest may ask; it can never reach any other.
func Add(path, name string, targets []string) (string, error) {
	if !nameRE.MatchString(name) {
		return "", ErrName
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	err := mutate(path, func(recs []record) ([]record, error) {
		for _, r := range recs {
			if r.Name == name && !r.Revoked {
				return nil, ErrExists
			}
		}
		return append(recs, record{Name: name, Hash: hex.EncodeToString(sum[:]), Targets: targets}), nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// Revoke marks a guest so its token stops working.
func Revoke(path, name string) error {
	if !nameRE.MatchString(name) {
		return ErrName
	}
	return mutate(path, func(recs []record) ([]record, error) {
		for i := range recs {
			if recs[i].Name == name && !recs[i].Revoked {
				recs[i].Revoked = true
				return recs, nil
			}
		}
		return nil, ErrNotFound
	})
}

// List returns names and revoked flags. It does not return hashes.
func List(path string) ([]Guest, error) {
	recs, err := read(path)
	if err != nil {
		return nil, err
	}
	out := make([]Guest, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.public())
	}
	return out, nil
}

// Check accepts a token. Unknown and revoked both return ErrDenied.
func Check(path, token string) (Guest, error) {
	recs, err := read(path)
	if err != nil {
		return Guest{}, err
	}
	sum := sha256.Sum256([]byte(token))
	presented := sum[:]
	var hit *record
	for i := range recs {
		raw, err := hex.DecodeString(recs[i].Hash)
		if err != nil || len(raw) != sha256.Size {
			raw = make([]byte, sha256.Size)
		}
		if subtle.ConstantTimeCompare(presented, raw) == 1 {
			copy := recs[i]
			hit = &copy
		}
	}
	if hit == nil || hit.Revoked {
		return Guest{}, ErrDenied
	}
	return hit.public(), nil
}

// MarkPending records that a guest with a valid token called before approval,
// so the host can see who is waiting.
func MarkPending(path, name string) error {
	return mutate(path, func(recs []record) ([]record, error) {
		for i := range recs {
			if recs[i].Name == name && !recs[i].Revoked && !recs[i].Approved {
				recs[i].Pending = true
			}
		}
		return recs, nil
	})
}

// Approve is the pairing step. It only exists as a local command: the server
// never calls it, so a remote caller cannot approve itself.
func Approve(path, name string) error {
	if !nameRE.MatchString(name) {
		return ErrName
	}
	return mutate(path, func(recs []record) ([]record, error) {
		for i := range recs {
			if recs[i].Name == name && !recs[i].Revoked {
				recs[i].Approved = true
				recs[i].Pending = false
				return recs, nil
			}
		}
		return nil, ErrNotFound
	})
}

func mutate(path string, fn func([]record) ([]record, error)) error {
	return filelock.With(path, func(f *os.File) error {
		recs, err := decode(f)
		if err != nil {
			return err
		}
		next, err := fn(recs)
		if err != nil {
			return err
		}
		body, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		body = append(body, '\n')
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := f.Truncate(0); err != nil {
			return err
		}
		_, err = f.Write(body)
		return err
	})
}

func read(path string) ([]record, error) {
	var recs []record
	err := filelock.With(path, func(f *os.File) error {
		var err error
		recs, err = decode(f)
		return err
	})
	return recs, err
}

func decode(f *os.File) ([]record, error) {
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var recs []record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}
