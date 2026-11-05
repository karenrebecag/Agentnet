//go:build unix

package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	skilldoc "github.com/karenrebecag/agentnet/adapters/claude-code"
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
	Home     string
	SkillDir string
	Install  bool
	Force    bool
	LookPath func(string) (string, error)
	Stdout   io.Writer
}

func (e env) init(args []string) (int, error) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	install := fs.Bool("install-claude-skill", false, "write the embedded Claude skill into the skills directory")
	force := fs.Bool("force", false, "replace a symlink or a different SKILL.md")
	if _, err := parse(fs, args); err != nil {
		return ExitError, err
	}
	cfg := initConfig{
		Home: e.store.Home, Install: *install, Force: *force,
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
	return installSkill(cfg.SkillDir, cfg.Force, cfg.Stdout)
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

func installSkill(destDir string, force bool, stdout io.Writer) error {
	if destDir == "" {
		return errors.New("missing skill directory")
	}
	// MkdirAll follows a symlink. Refuse, or remove only the link, before that.
	lst, err := os.Lstat(destDir)
	switch {
	case err == nil && lst.Mode()&os.ModeSymlink != 0:
		if !force {
			return errors.New("skill directory is a symlink; rerun with --force to replace the link with a real directory")
		}
		if err := os.Remove(destDir); err != nil {
			return err
		}
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(destDir, 0o700); err != nil {
		return err
	}
	dest := filepath.Join(destDir, "SKILL.md")
	fileInfo, err := os.Lstat(dest)
	if err == nil && fileInfo.Mode()&os.ModeSymlink != 0 {
		if !force {
			return errors.New("SKILL.md is a symlink; rerun with --force")
		}
		if err := os.Remove(dest); err != nil {
			return err
		}
	} else if err == nil {
		body, err := os.ReadFile(dest)
		if err != nil {
			return err
		}
		if bytes.Equal(body, skilldoc.Markdown) {
			_, err = fmt.Fprintf(stdout, "skill %s\n", dest)
			return err
		}
		if !force {
			return errors.New("SKILL.md already exists and differs; rerun with --force")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(dest, skilldoc.Markdown, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "skill %s\n", dest)
	return err
}
