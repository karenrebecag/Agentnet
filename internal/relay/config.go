//go:build unix

package relay

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

// File is ~/.agentnet/relay.json. link reads this and never the admin file.
type File struct {
	URL   string `json:"url"`
	Node  string `json:"node,omitempty"`
	Token string `json:"token,omitempty"`
}

type adminFile struct {
	Token string `json:"token"`
}

func configPath(home string) string { return filepath.Join(home, "relay.json") }
func adminPath(home string) string  { return filepath.Join(home, "relay-admin.json") }

func Load(home string) (File, error) {
	raw, err := os.ReadFile(configPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, err
	}
	return f, nil
}

func Save(home string, f File) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return writePrivate(configPath(home), append(raw, '\n'))
}

func loadAdmin(home string) (adminFile, error) {
	raw, err := os.ReadFile(adminPath(home))
	if err != nil {
		return adminFile{}, err
	}
	var f adminFile
	err = json.Unmarshal(raw, &f)
	return f, err
}

// WriteAdminToken creates a fresh admin token, stores it in relay-admin.json,
// and returns it. The caller prints it once; link never reads this file.
func WriteAdminToken(home string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := "an_" + base64.RawURLEncoding.EncodeToString(buf)
	raw, err := json.Marshal(adminFile{Token: tok})
	if err != nil {
		return "", err
	}
	if err := writePrivate(adminPath(home), append(raw, '\n')); err != nil {
		return "", err
	}
	return tok, nil
}

// Init stores an https relay URL. An existing node and token are kept.
func Init(home, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("relay url must be https")
	}
	u.Path, u.RawQuery, u.Fragment = "", "", ""
	f, err := Load(home)
	if err != nil {
		return err
	}
	f.URL = u.String()
	return Save(home, f)
}

func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// umask would otherwise leave the directory traversable.
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
