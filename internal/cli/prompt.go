package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/nickzelei/unifi-cli/internal/unifi"
)

// prompter asks questions on stderr and reads one line per answer from stdin.
type prompter struct {
	ctx context.Context
	in  *bufio.Reader
	out io.Writer
	tty *os.File // stdin, when it is a terminal whose echo stty can turn off
}

func newPrompter(ctx context.Context, s IO) *prompter {
	p := &prompter{ctx: ctx, in: bufio.NewReader(s.In), out: s.Err}
	if f, ok := s.In.(*os.File); ok && s.InTTY {
		p.tty = f
	}
	return p
}

// line reads one line, returning early if ctx is cancelled (Ctrl-C).
func (p *prompter) line() (string, error) {
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := p.in.ReadString('\n')
		ch <- result{s, err}
	}()
	select {
	case <-p.ctx.Done():
		return "", p.ctx.Err()
	case r := <-ch:
		switch {
		case r.err == nil, errors.Is(r.err, io.EOF) && r.s != "":
			return strings.TrimSpace(r.s), nil
		case errors.Is(r.err, io.EOF):
			// Also reached when stdin is /dev/null, which looks like a terminal.
			return "", usagef("input ended before every question was answered (pass flags instead: `unifi config init --help`)")
		}
		return "", r.err
	}
}

// ask prints q, with def in brackets if set, and returns the trimmed answer
// ("" if the user just pressed enter).
func (p *prompter) ask(q, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", q, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", q)
	}
	return p.line()
}

// secret is ask without echoing the answer, where stty can arrange that.
func (p *prompter) secret(q string) (string, error) {
	if p.tty != nil && stty(p.tty, "-echo") == nil {
		defer func() {
			_ = stty(p.tty, "echo")
			fmt.Fprintln(p.out)
		}()
	} else {
		q += " (input is visible)"
	}
	return p.ask(q, "")
}

func stty(tty *os.File, arg string) error {
	cmd := exec.Command("stty", arg)
	cmd.Stdin = tty
	return cmd.Run()
}

// initAnswers points at the `config init` flags the interactive mode fills.
type initAnswers struct {
	name, host, keyCmd, key, site *string
}

// promptInit asks for the values `config init` otherwise takes as flags. A
// blank answer keeps the profile's current value, as an unset flag would.
func promptInit(ctx context.Context, s IO, f *unifi.File, a initAnswers) error {
	p := newPrompter(ctx, s)
	fmt.Fprintln(p.out, "Setting up a unifi profile. Press enter to accept [defaults].")

	name, err := p.ask("Profile name", *a.name)
	if err != nil {
		return err
	}
	if name != "" {
		*a.name = name
	}
	cur := f.Profiles[*a.name]

	for *a.host == "" {
		if *a.host, err = p.ask("Console address, e.g. 192.168.1.1", cur.Host); err != nil {
			return err
		}
		if cur.Host != "" || cur.BaseURL != "" {
			break // blank keeps the current address
		}
	}

	fmt.Fprint(p.out, `
Create an API key in UniFi Network > Settings > Control Plane > Integrations.
How should unifi get it?
  1) run a command that prints it, e.g. "op read op://Private/UniFi/credential" (recommended)
  2) paste the key now (stored in plain text in the config file)
`)
	def := "1"
	if cur.APIKey != "" {
		def = "2"
	}
	choice := ""
	for choice != "1" && choice != "2" {
		if choice, err = p.ask("Choice", def); err != nil {
			return err
		}
		if choice == "" {
			choice = def
		}
	}
	for *a.keyCmd == "" && *a.key == "" {
		keep := false // a blank answer keeps the current value
		if choice == "1" {
			*a.keyCmd, err = p.ask("Command", cur.APIKeyCmd)
			keep = cur.APIKeyCmd != ""
		} else {
			q := "API key"
			if cur.APIKey != "" {
				q += " (blank keeps the current one)"
			}
			*a.key, err = p.secret(q)
			keep = cur.APIKey != ""
		}
		if err != nil {
			return err
		}
		if keep {
			break
		}
	}

	if *a.site, err = p.ask("\nSite (id, internal reference or name; blank for the only site)", cur.Site); err != nil {
		return err
	}
	fmt.Fprintln(p.out)
	return nil
}
