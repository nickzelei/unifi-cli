package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickzelei/unifi-cli/skills"
)

func TestSkillInstall(t *testing.T) {
	want, err := skills.FS.ReadFile("unifi/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	skillMD := filepath.Join(dir, "unifi", "SKILL.md")
	for range 2 { // installing over an earlier copy is fine
		code, out, errb := run(t, "", "skill", "install", "--dir", dir)
		if code != 0 || !strings.Contains(out, skillMD) {
			t.Fatalf("exit %d: %s%s", code, out, errb)
		}
		got, err := os.ReadFile(skillMD)
		if err != nil || string(got) != string(want) {
			t.Fatalf("installed SKILL.md differs (err %v)", err)
		}
	}

	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if code, out, _ := run(t, "", "skill", "path"); code != 0 || strings.TrimSpace(out) != filepath.Join(dir, "skills", "unifi") {
		t.Errorf("skill path: exit %d, %q", code, out)
	}
	if code, out, _ := run(t, "", "skill", "show"); code != 0 || out != string(want) {
		t.Errorf("skill show: exit %d", code)
	}
}

func TestSkillInstallSymlink(t *testing.T) {
	dir, checkout := t.TempDir(), t.TempDir()
	link := filepath.Join(dir, "unifi")
	if err := os.Symlink(checkout, link); err != nil {
		t.Fatal(err)
	}
	code, _, errb := run(t, "", "skill", "install", "--dir", dir)
	if code != 0 || !strings.Contains(errb, "is a symlink") {
		t.Fatalf("exit %d: %s", code, errb)
	}
	if _, err := os.Stat(filepath.Join(checkout, "SKILL.md")); err == nil {
		t.Fatal("wrote through the symlink")
	}

	if code, _, errb := run(t, "", "skill", "install", "--dir", dir, "--force"); code != 0 {
		t.Fatalf("--force: exit %d: %s", code, errb)
	}
	fi, err := os.Lstat(link)
	if err != nil || !fi.IsDir() {
		t.Fatalf("after --force: %v, %v", fi, err)
	}

	broken := t.TempDir()
	if err := os.Symlink(filepath.Join(broken, "gone"), filepath.Join(broken, "unifi")); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := run(t, "", "skill", "install", "--dir", broken); code != exitErr || !strings.Contains(errb, "broken symlink") {
		t.Errorf("broken symlink: exit %d: %s", code, errb)
	}
}
