package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"

	"github.com/nickzelei/unifi-cli/skills"
)

// skillName is the embedded skill's directory in skills.FS and on disk.
const skillName = "unifi"

func skillCmd(args []string, io IO) error {
	if len(args) == 0 || hasHelpFlag(args[:1]) {
		fmt.Fprint(io.Out, `Install the agent skill that teaches AI agents to use this CLI.

Usage:
  unifi skill install [--dir <skills dir>] [--force]
  unifi skill show
  unifi skill path [--dir <skills dir>]

install writes the skill bundled with this binary to <dir>/unifi, overwriting
earlier copies, so re-run it after upgrading. The default dir is
$CLAUDE_CONFIG_DIR/skills, else ~/.claude/skills (Claude Code); point --dir at
another agent's skills directory to install it there.
`)
		return nil
	}
	switch args[0] {
	case "install":
		return skillInstall(args[1:], io)
	case "show":
		b, err := skills.FS.ReadFile(skillName + "/SKILL.md")
		if err != nil {
			return err
		}
		_, err = io.Out.Write(b)
		return err
	case "path":
		fs := flag.NewFlagSet("skill path", flag.ContinueOnError)
		fs.SetOutput(io.Err)
		dir := fs.String("dir", "", "skills directory (default: $CLAUDE_CONFIG_DIR/skills or ~/.claude/skills)")
		if err := fs.Parse(args[1:]); err != nil {
			return flagErr(err, "skill path")
		}
		target, err := skillTarget(*dir)
		if err != nil {
			return err
		}
		fmt.Fprintln(io.Out, target)
		return nil
	}
	return usagef("unknown skill subcommand %q (install, show, path)", args[0])
}

// skillTarget returns the directory the skill is installed to.
func skillTarget(dir string) (string, error) {
	if dir == "" {
		if c := os.Getenv("CLAUDE_CONFIG_DIR"); c != "" {
			dir = filepath.Join(c, "skills")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dir = filepath.Join(home, ".claude", "skills")
		}
	}
	return filepath.Join(dir, skillName), nil
}

func skillInstall(args []string, io IO) error {
	fs := flag.NewFlagSet("skill install", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	dir := fs.String("dir", "", "skills directory (default: $CLAUDE_CONFIG_DIR/skills or ~/.claude/skills)")
	force := fs.Bool("force", false, "replace a symlinked install (e.g. from `mise run skill:install`) with a copy")
	if err := fs.Parse(args); err != nil {
		return flagErr(err, "skill install")
	}
	if fs.NArg() > 0 {
		return usagef("unexpected argument %q", fs.Arg(0))
	}
	target, err := skillTarget(*dir)
	if err != nil {
		return err
	}

	// A symlink usually points at a checkout (`mise run skill:install`), which
	// already tracks the repo's copy. Leave it alone unless told otherwise.
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		dest, _ := os.Readlink(target)
		if !*force {
			if _, err := os.Stat(target); err != nil {
				return fmt.Errorf("%s is a broken symlink to %s; pass --force to replace it", target, dest)
			}
			fmt.Fprintf(io.Err, "unifi: skipped: %s is a symlink to %s, which already provides the skill; pass --force to replace it with this binary's copy\n", target, dest)
			return nil
		}
		if err := os.Remove(target); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return err
	}

	var files []string
	err = iofs.WalkDir(skills.FS, skillName, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(skillName, filepath.FromSlash(p))
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := skills.FS.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
		files = append(files, dst)
		return nil
	})
	if err != nil {
		return fmt.Errorf("install skill: %w", err)
	}
	b, err := json.Marshal(map[string]any{"skill": skillName, "dir": target, "files": files, "version": Version()})
	if err != nil {
		return err
	}
	return emit(io, b, true, nil)
}
