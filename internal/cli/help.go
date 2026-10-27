package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/nickzelei/unifi-cli/internal/spec"
)

// bodySchemaDepth is how many $ref levels command help inlines.
const bodySchemaDepth = 2

func opHelp(w io.Writer, op *spec.Op) error {
	fmt.Fprintf(w, "%s\n", op.Summary)
	if op.Description != "" && op.Description != op.Summary {
		fmt.Fprintf(w, "\n%s\n", op.Description)
	}
	fmt.Fprintf(w, "\nUsage:\n  unifi %s [flags]\n", usageLine(op))

	if len(op.Args) > 0 {
		fmt.Fprintf(w, "\nArguments:\n")
		for _, a := range op.Args {
			doc := paramDoc(a)
			if l := lookupOp(op, a); l != nil {
				doc += fmt.Sprintf("; or a name/MAC/IP matched against `unifi %s` (one extra request).", l.Name())
			}
			fmt.Fprintf(w, "  %-28s %s\n", "<"+a.Flag+">", doc)
		}
	}

	fmt.Fprintf(w, "\nFlags:\n")
	for _, f := range op.Flags {
		fmt.Fprintf(w, "  %-28s %s\n", "--"+f.Flag+" <"+f.Type+">", paramDoc(f))
	}
	if op.Paged {
		fmt.Fprintf(w, "  %-28s %s\n", "--all", fmt.Sprintf("fetch every page (%d per request) and merge into one page", maxPageLimit))
	}
	if op.Body {
		req := "optional"
		if op.BodyRequired {
			req = "required"
		}
		fmt.Fprintf(w, "  %-28s %s\n", "--data <json>", "request body ("+req+"): inline JSON, @file, or - for stdin")
	}
	if op.Destructive {
		fmt.Fprintf(w, "  %-28s %s\n", "--yes", "confirm this destructive operation (required)")
	}
	fmt.Fprintf(w, "  %-28s %s\n", "global", "--site --profile --fields --pretty --dry-run --timeout")

	if op.Body {
		sch, err := spec.RequestSchema(op)
		if err != nil {
			return err
		}
		if sch != nil {
			exp, err := spec.Expand(sch, bodySchemaDepth)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(exp, "", "  ")
			fmt.Fprintf(w, "\nRequest body schema (refs deeper than %d levels: `unifi schema <name>`):\n%s\n", bodySchemaDepth, b)
		}
	}
	if op.Response != "" {
		fmt.Fprintf(w, "\nResponse: %s (`unifi schema %q`)\n", op.Response, op.Response)
	}
	fmt.Fprintf(w, "\nAPI: %s %s (%s)\n", op.Method, op.Path, op.ID)
	return nil
}

func paramDoc(p spec.Param) string {
	var parts []string
	if p.Description != "" {
		parts = append(parts, strings.ReplaceAll(p.Description, "\n", " "))
	}
	if p.Format != "" {
		parts = append(parts, "format: "+p.Format)
	}
	if p.Default != "" {
		parts = append(parts, "default: "+p.Default)
	}
	if p.Required && p.In == "query" {
		parts = append(parts, "required")
	}
	return strings.Join(parts, "; ")
}

func schemaCmd(args []string, io IO) error {
	fs := flag.NewFlagSet("schema", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	fs.Usage = func() {
		fmt.Fprintln(io.Out, "Usage: unifi schema <name> [--depth N]\n       unifi schema --list\n\nNames match ignoring case, spaces and dashes.")
	}
	depth := fs.Int("depth", 1, "levels of $ref to inline")
	list := fs.Bool("list", false, "list all schema names")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return flagErr(err, "schema")
	}
	if *list {
		names, err := spec.SchemaNames()
		if err != nil {
			return err
		}
		fmt.Fprintln(io.Out, strings.Join(names, "\n"))
		return nil
	}
	if len(pos) == 0 {
		return usagef("usage: unifi schema <name> [--depth N] | --list")
	}
	name, sch, err := spec.LookupSchema(strings.Join(pos, " "))
	if err != nil {
		return err
	}
	exp, err := spec.Expand(sch, *depth)
	if err != nil {
		return err
	}
	if m, ok := exp.(map[string]any); ok {
		m["x-schema"] = name
	}
	b, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(io.Out, "%s\n", b)
	return err
}
