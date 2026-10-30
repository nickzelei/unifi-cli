// Package cli implements the unifi command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/nickzelei/unifi-cli/internal/spec"
	"github.com/nickzelei/unifi-cli/internal/unifi"
)

// version is set with -ldflags "-X github.com/nickzelei/unifi-cli/internal/cli.version=v1.2.3".
var version = ""

// Version returns the build version.
func Version() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// Exit codes.
const (
	exitOK    = 0
	exitErr   = 1 // runtime or API error
	exitUsage = 2 // bad invocation, or a destructive op without --yes
)

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// IO bundles the process streams so tests can substitute them.
type IO struct {
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	OutTTY bool
}

// StdIO returns the real process streams.
func StdIO() IO {
	return IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, OutTTY: isTerminal(os.Stdout)}
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// Run executes the CLI and returns the process exit code.
func Run(ctx context.Context, args []string, io IO) int {
	unifi.UserAgent = "unifi-cli/" + Version()
	err := dispatch(ctx, args, io)
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	}
	fmt.Fprintln(io.Err, "unifi:", err)
	var ue usageError
	if errors.As(err, &ue) {
		return exitUsage
	}
	return exitErr
}

func dispatch(ctx context.Context, args []string, io IO) error {
	if len(args) == 0 {
		rootHelp(io.Out)
		return nil
	}
	switch args[0] {
	case "-h", "--help", "-help":
		rootHelp(io.Out)
		return nil
	case "help":
		return helpCmd(args[1:], io)
	case "version", "--version":
		fmt.Fprintf(io.Out, "unifi %s (UniFi Network API spec %s)\n", Version(), spec.SpecVersion)
		return nil
	case "commands":
		return commandsCmd(args[1:], io)
	case "schema":
		return schemaCmd(args[1:], io)
	case "api":
		return apiCmd(ctx, args[1:], io)
	case "config":
		return configCmd(ctx, args[1:], io)
	case "doctor":
		return doctorCmd(ctx, args[1:], io)
	}
	if op, n := spec.Find(args); op != nil {
		return runOp(ctx, op, args[n:], io)
	}
	words := leadingWords(args)
	if ops := opsWithPrefix(words); len(ops) > 0 {
		groupHelp(io.Out, words, ops)
		if hasHelpFlag(args) {
			return nil
		}
		return usagef("%q needs a subcommand", strings.Join(words, " "))
	}
	return usagef("unknown command %q (run `unifi commands` to list all)", strings.Join(words, " "))
}

func leadingWords(args []string) []string {
	var w []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		w = append(w, a)
	}
	return w
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "--help", "-help":
			return true
		}
	}
	return false
}

func opsWithPrefix(words []string) []*spec.Op {
	var out []*spec.Op
	for i := range spec.Ops {
		op := &spec.Ops[i]
		if len(words) >= len(op.Command) {
			continue
		}
		match := true
		for j, w := range words {
			if op.Command[j] != w {
				match = false
				break
			}
		}
		if match {
			out = append(out, op)
		}
	}
	return out
}

func helpCmd(args []string, io IO) error {
	if len(args) == 0 {
		rootHelp(io.Out)
		return nil
	}
	if len(args) == 1 && (args[0] == "filter" || args[0] == "filtering") {
		doc, err := spec.Topic("Filtering")
		if err != nil {
			return err
		}
		fmt.Fprintln(io.Out, doc)
		return nil
	}
	if op, n := spec.Find(args); op != nil && n == len(args) {
		return opHelp(io.Out, op)
	}
	if ops := opsWithPrefix(args); len(ops) > 0 {
		groupHelp(io.Out, args, ops)
		return nil
	}
	return usagef("unknown command %q", strings.Join(args, " "))
}

func rootHelp(w io.Writer) {
	fmt.Fprintf(w, `unifi - CLI for the UniFi Network integration API (spec %s)

Usage:
  unifi <command> [args] [flags]

API commands (run "unifi <command> --help" for details):
`, spec.SpecVersion)
	groups := map[string][]string{}
	var order []string
	for i := range spec.Ops {
		op := &spec.Ops[i]
		g := op.Group()
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], op.Verb())
	}
	sort.Strings(order)
	for _, g := range order {
		fmt.Fprintf(w, "  %-34s %s\n", g, strings.Join(groups[g], " "))
	}
	fmt.Fprint(w, `
Other commands:
  commands [--json]                  list every API command with its summary
  schema <name> [--depth N]          print a schema from the API spec (--list for all)
  api <METHOD> <path> [flags]        send a raw request, e.g. unifi api GET /v1/sites/{siteId}/wans
  config init|show|path              manage connection profiles
  doctor                             check config, TLS, API key, site and spec version
  help filter                        --filter syntax for list commands
  version                            print versions

Global flags (after the command):
  --site <id|ref|name>   site to use (default: config, $UNIFI_SITE, or the only site)
  --profile <name>       config profile (default: $UNIFI_PROFILE or default_profile)
  --fields a,b.c         keep only these fields in the output (applies to each item of a page)
  --pretty               indent JSON output (default when stdout is a terminal)
  --dry-run              print the request instead of sending it
  --timeout <dur>        request timeout (default 30s)

Output is JSON. Errors go to stderr. Exit codes: 0 ok, 1 error, 2 usage.
`)
}

func groupHelp(w io.Writer, words []string, ops []*spec.Op) {
	fmt.Fprintf(w, "Commands under %q:\n\n", strings.Join(words, " "))
	for _, op := range ops {
		fmt.Fprintf(w, "  %-44s %s\n", usageLine(op), op.Summary)
	}
}

func commandsCmd(args []string, io IO) error {
	fs := flag.NewFlagSet("commands", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	asJSON := fs.Bool("json", false, "emit JSON with method, path and flags for each command")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*asJSON {
		for i := range spec.Ops {
			op := &spec.Ops[i]
			fmt.Fprintf(io.Out, "%-56s %s\n", usageLine(op), op.Summary)
		}
		return nil
	}
	type entry struct {
		Command     string   `json:"command"`
		Summary     string   `json:"summary"`
		Method      string   `json:"method"`
		Path        string   `json:"path"`
		Args        []string `json:"args,omitempty"`
		Flags       []string `json:"flags,omitempty"`
		Body        bool     `json:"body,omitempty"`
		Paged       bool     `json:"paged,omitempty"`
		Destructive bool     `json:"destructive,omitempty"`
	}
	out := make([]entry, 0, len(spec.Ops))
	for i := range spec.Ops {
		op := &spec.Ops[i]
		e := entry{Command: op.Name(), Summary: op.Summary, Method: op.Method, Path: op.Path, Body: op.Body, Paged: op.Paged, Destructive: op.Destructive}
		for _, a := range op.Args {
			e.Args = append(e.Args, a.Flag)
		}
		for _, f := range op.Flags {
			e.Flags = append(e.Flags, f.Flag)
		}
		out = append(out, e)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(io.Out, "%s\n", b)
	return err
}

func usageLine(op *spec.Op) string {
	var b strings.Builder
	b.WriteString(op.Name())
	for _, a := range op.Args {
		fmt.Fprintf(&b, " <%s>", a.Flag)
	}
	for _, f := range op.Flags {
		if f.Required {
			fmt.Fprintf(&b, " --%s <%s>", f.Flag, f.Type)
		}
	}
	if op.BodyRequired {
		b.WriteString(" --data <json>")
	}
	if op.Destructive {
		b.WriteString(" --yes")
	}
	return b.String()
}

// globals are the flags every API command accepts.
type globals struct {
	profile string
	site    string
	fields  string
	pretty  bool
	dryRun  bool
	yes     bool
	timeout time.Duration
}

func (g *globals) register(fs *flag.FlagSet) {
	fs.StringVar(&g.profile, "profile", "", "config profile")
	fs.StringVar(&g.site, "site", "", "site id, internal reference, or name")
	fs.StringVar(&g.fields, "fields", "", "comma-separated fields to keep")
	fs.BoolVar(&g.pretty, "pretty", false, "indent JSON output")
	fs.BoolVar(&g.dryRun, "dry-run", false, "print the request instead of sending it")
	fs.BoolVar(&g.yes, "yes", false, "confirm a destructive operation")
	fs.DurationVar(&g.timeout, "timeout", 30*time.Second, "request timeout")
}

func (g *globals) fieldList() []string {
	if g.fields == "" {
		return nil
	}
	var out []string
	for _, f := range strings.Split(g.fields, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseInterleaved parses flags that may appear before, between, or after
// positional args. Everything after "--" is positional.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return append(pos, tail...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func flagErr(err error, cmd string) error {
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	return usagef("%v (see `unifi %s --help`)", err, cmd)
}
