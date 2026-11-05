//go:build unix

package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skilldoc "github.com/karenrebecag/agentnet/adapters/claude-code"
)

func TestInitCreatesHomeAndKeepsExistingFiles(t *testing.T) {
	home := filepath.Join(t.TempDir(), "net")
	var out bytes.Buffer
	if err := runInit(initConfig{Home: home, Stdout: &out, LookPath: missing}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if !strings.Contains(out.String(), "home "+home+" created") {
		t.Fatalf("stdout: %s", out.String())
	}

	kept := filepath.Join(home, "inbox", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte("stay"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runInit(initConfig{Home: home, Stdout: &out, LookPath: missing}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(kept)
	if err != nil || string(body) != "stay" {
		t.Fatalf("existing file: %q %v", body, err)
	}
	info, err = os.Stat(home)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("tightened mode: %v", info)
	}
	if !strings.Contains(out.String(), "present") {
		t.Fatalf("stdout: %s", out.String())
	}
}

func TestInitRefusesNonDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "net")
	if err := os.WriteFile(home, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runInit(initConfig{Home: home, Stdout: ioDiscard{}, LookPath: missing})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestInitPrintsJoinOnlyForFoundTools(t *testing.T) {
	home := filepath.Join(t.TempDir(), "net")
	var out bytes.Buffer
	look := func(name string) (string, error) {
		if name == "claude" || name == "gemini" {
			return "/opt/bin/" + name, nil
		}
		return "", errors.New("missing")
	}
	if err := runInit(initConfig{Home: home, Stdout: &out, LookPath: look}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"claude\tagentnet join name --kind claude\n",
		"gemini\tagentnet join name --kind gemini\n",
		"grok\tnot on PATH\n",
		"codex\tnot on PATH\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "/opt/bin/") {
		t.Fatalf("printed a tool path: %s", got)
	}
}

func TestInitInstallsSkillOnlyWhenAsked(t *testing.T) {
	home := filepath.Join(t.TempDir(), "net")
	dest := filepath.Join(t.TempDir(), "skills")
	var out bytes.Buffer
	if err := runInit(initConfig{Home: home, SkillDir: dest, Stdout: &out, LookPath: missing}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("skill copied without the flag")
	}

	out.Reset()
	if err := runInit(initConfig{
		Home: home, SkillDir: dest, Install: true, Stdout: &out, LookPath: missing,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil || !bytes.Equal(got, skilldoc.Markdown) {
		t.Fatalf("skill: %q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dest, "SKILL.md"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("skill mode: %v", info)
	}
	dirInfo, err := os.Stat(dest)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("skill dir mode: %v", dirInfo)
	}
}

func TestInitRefusesSymlinkUnlessForced(t *testing.T) {
	home := filepath.Join(t.TempDir(), "net")
	target := filepath.Join(t.TempDir(), "prototype")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	const old = "prototype-skill\n"
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "agentnet")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := runInit(initConfig{
		Home: home, SkillDir: link, Install: true, Stdout: ioDiscard{}, LookPath: missing,
	})
	if err == nil {
		t.Fatal("expected symlink refusal")
	}
	body, readErr := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if readErr != nil || string(body) != old {
		t.Fatalf("target changed: %q %v", body, readErr)
	}
	info, statErr := os.Lstat(link)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced without --force")
	}

	if err := runInit(initConfig{
		Home: home, SkillDir: link, Install: true, Force: true, Stdout: ioDiscard{}, LookPath: missing,
	}); err != nil {
		t.Fatal(err)
	}
	info, statErr = os.Lstat(link)
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("force did not replace the link with a directory: %v", info)
	}
	got, err := os.ReadFile(filepath.Join(link, "SKILL.md"))
	if err != nil || !bytes.Equal(got, skilldoc.Markdown) {
		t.Fatalf("installed skill: %q %v", got, err)
	}
	body, readErr = os.ReadFile(filepath.Join(target, "SKILL.md"))
	if readErr != nil || string(body) != old {
		t.Fatalf("force followed the link: %q %v", body, readErr)
	}
}

func TestInitRefusesDifferentSkillUnlessForced(t *testing.T) {
	home := filepath.Join(t.TempDir(), "net")
	dest := t.TempDir()
	const old = "other-skill\n"
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runInit(initConfig{
		Home: home, SkillDir: dest, Install: true, Stdout: ioDiscard{}, LookPath: missing,
	})
	if err == nil {
		t.Fatal("expected existing-file refusal")
	}
	body, readErr := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if readErr != nil || string(body) != old {
		t.Fatalf("file changed: %q %v", body, readErr)
	}
	if err := runInit(initConfig{
		Home: home, SkillDir: dest, Install: true, Force: true, Stdout: ioDiscard{}, LookPath: missing,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil || !bytes.Equal(got, skilldoc.Markdown) {
		t.Fatalf("forced skill: %q %v", got, err)
	}
}

func missing(string) (string, error) { return "", errors.New("missing") }

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
