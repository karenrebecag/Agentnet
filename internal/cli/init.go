//go:build unix

package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// initTools are the CLIs init knows how to print a join line for.
// Detection is LookPath only. init never installs them.
var initTools = []struct{ bin, kind string }{
	{"claude", "claude"},
	{"grok", "grok"},
	{"codex", "codex"},
	{"gemini", "gemini"},
}

type initConfig struct {
	Home      string
	SkillRoot string
	SkillDir  string
	Install   bool
	LookPath  func(string) (string, error)
	Stdout    io.Writer
}

func (e env) init(args []string) (int, error) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	install := fs.Bool("install-claude-skill", false, "copy adapters/claude-code/SKILL.md into the Claude skills directory")
	if _, err := parse(fs, args); err != nil {
		return ExitError, err
	}
	cfg := initConfig{
		Home: e.store.Home, SkillRoot: ".", Install: *install,
		LookPath: exec.LookPath, Stdout: e.stdout,
	}
	if *install {
		dir, err := claudeSkillDir()
		if err != nil {
			return ExitError, err
		}
		cfg.SkillDir = dir
	}
	return ExitOK, runInit(cfg)
}

func claudeSkillDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills", "agentnet"), nil
}

func runInit(cfg initConfig) error {
	created, err := ensureHome(cfg.Home)
	if err != nil {
		return err
	}
	state := "present"
	if created {
		state = "created"
	}
	if _, err := fmt.Fprintf(cfg.Stdout, "home %s %s\n", cfg.Home, state); err != nil {
		return err
	}
	look := cfg.LookPath
	if look == nil {
		look = exec.LookPath
	}
	for _, tool := range initTools {
		if _, err := look(tool.bin); err != nil {
			if _, err := fmt.Fprintf(cfg.Stdout, "%s\tnot on PATH\n", tool.bin); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(cfg.Stdout, "%s\tagentnet join name --kind %s\n", tool.bin, tool.kind); err != nil {
			return err
		}
	}
	if !cfg.Install {
		return nil
	}
	return copySkill(cfg.SkillRoot, cfg.SkillDir, cfg.Stdout)
}

// ensureHome creates the data directory at 0700. An existing directory is kept,
// including its inbox, and only its own mode is tightened. It is not emptied.
func ensureHome(home string) (bool, error) {
	info, err := os.Stat(home)
	if err == nil {
		if !info.IsDir() {
			return false, fmt.Errorf("%s exists and is not a directory", home)
		}
		if info.Mode().Perm() != 0o700 {
			if err := os.Chmod(home, 0o700); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return false, err
	}
	// MkdirAll is masked by umask, so the mode has to be set again.
	if err := os.Chmod(home, 0o700); err != nil {
		return false, err
	}
	return true, nil
}

func copySkill(root, destDir string, stdout io.Writer) error {
	if destDir == "" {
		return errors.New("missing skill directory")
	}
	src, err := findSkill(root)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(destDir, 0o700); err != nil {
		return err
	}
	dest := filepath.Join(destDir, "SKILL.md")
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "skill %s\n", dest)
	return err
}

func findSkill(start string) (string, error) {
	if start == "" {
		start = "."
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for range 8 {
		cand := filepath.Join(dir, "adapters", "claude-code", "SKILL.md")
		info, err := os.Stat(cand)
		if err == nil && !info.IsDir() {
			return cand, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("adapters/claude-code/SKILL.md not found; run init from the agentnet repository")
}
