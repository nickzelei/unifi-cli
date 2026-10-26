package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/nickzelei/unifi-cli/internal/spec"
	"github.com/nickzelei/unifi-cli/internal/unifi"
)

const maxPageLimit = 200

func runOp(ctx context.Context, op *spec.Op, args []string, io IO) error {
	fs := flag.NewFlagSet(op.Name(), flag.ContinueOnError)
	fs.SetOutput(io.Err)
	fs.Usage = func() { _ = opHelp(io.Out, op) }
	var g globals
	g.register(fs)
	query := make(map[string]*string, len(op.Flags))
	for _, p := range op.Flags {
		query[p.Name] = fs.String(p.Flag, "", p.Description)
	}
	var data string
	if op.Body {
		fs.StringVar(&data, "data", "", "request body: JSON, @file, or - for stdin")
	}
	var all bool
	if op.Paged {
		fs.BoolVar(&all, "all", false, "fetch every page")
	}

	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return flagErr(err, op.Name())
	}
	if len(pos) != len(op.Args) {
		return usagef("usage: unifi %s (got %d argument(s), want %d)", usageLine(op), len(pos), len(op.Args))
	}

	q := url.Values{}
	for _, p := range op.Flags {
		v := *query[p.Name]
		if v == "" {
			if p.Required {
				return usagef("--%s is required (see `unifi %s --help`)", p.Flag, op.Name())
			}
			continue
		}
		if err := checkType(p, v); err != nil {
			return err
		}
		q.Set(p.Name, v)
	}

	var body []byte
	if op.Body {
		if body, err = readData(data, io.In); err != nil {
			return err
		}
	}
	if op.BodyRequired && body == nil {
		return usagef("--data is required; run `unifi %s --help` for the body schema", op.Name())
	}
	if op.Destructive && !g.yes && !g.dryRun {
		return usagef("%q is destructive (%s %s); re-run with --yes to confirm", op.Name(), op.Method, op.Path)
	}

	cfg, err := unifi.Load(g.profile)
	if err != nil {
		return err
	}
	c, err := unifi.NewClient(cfg.Profile, g.timeout)
	if err != nil {
		return err
	}

	path := op.Path
	if op.SiteScoped {
		site := g.site
		if site == "" {
			site = cfg.Profile.Site
		}
		id, err := c.ResolveSite(ctx, site)
		if err != nil {
			return err
		}
		path = strings.Replace(path, "{siteId}", url.PathEscape(id), 1)
	}
	for i, a := range op.Args {
		if err := checkType(a, pos[i]); err != nil {
			return err
		}
		path = strings.Replace(path, "{"+a.Name+"}", url.PathEscape(pos[i]), 1)
	}

	if g.dryRun {
		return printDryRun(io, op.Method, c.URL(path, q), body)
	}

	var out []byte
	if all {
		out, err = fetchAll(ctx, c, path, q)
	} else {
		out, err = c.Do(ctx, op.Method, path, q, body)
	}
	if err != nil {
		return err
	}
	return emit(io, out, g.pretty, g.fieldList())
}

func checkType(p spec.Param, v string) error {
	name := "--" + p.Flag
	if p.In == "path" {
		name = "<" + p.Flag + ">"
	}
	switch p.Type {
	case "integer":
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return usagef("%s must be an integer, got %q", name, v)
		}
	case "boolean":
		if _, err := strconv.ParseBool(v); err != nil {
			return usagef("%s must be true or false, got %q", name, v)
		}
	}
	if p.Format == "uuid" && !unifi.IsUUID(v) {
		return usagef("%s must be a UUID, got %q", name, v)
	}
	return nil
}

// readData resolves --data: "" (none), "-" (stdin), "@path" (file), or inline JSON.
func readData(data string, stdin io.Reader) ([]byte, error) {
	var b []byte
	var err error
	switch {
	case data == "":
		return nil, nil
	case data == "-":
		b, err = io.ReadAll(stdin)
	case strings.HasPrefix(data, "@"):
		b, err = os.ReadFile(data[1:])
	default:
		b = []byte(data)
	}
	if err != nil {
		return nil, fmt.Errorf("read --data: %w", err)
	}
	if !json.Valid(b) {
		return nil, usagef("--data is not valid JSON")
	}
	return b, nil
}

func printDryRun(io IO, method, u string, body []byte) error {
	req := map[string]any{"method": method, "url": u}
	if body != nil {
		req["body"] = json.RawMessage(body)
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return emit(io, b, true, nil)
}

// fetchAll walks offset/limit pages and returns one page object holding
// every item.
func fetchAll(ctx context.Context, c *unifi.Client, path string, q url.Values) ([]byte, error) {
	q = cloneValues(q)
	if q.Get("limit") == "" {
		q.Set("limit", strconv.Itoa(maxPageLimit))
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	var items []json.RawMessage
	var total int
	for {
		q.Set("offset", strconv.Itoa(offset))
		b, err := c.Do(ctx, "GET", path, q, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Count      int               `json:"count"`
			TotalCount int               `json:"totalCount"`
			Data       []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(b, &page); err != nil {
			return nil, fmt.Errorf("decode page: %w", err)
		}
		items = append(items, page.Data...)
		total = page.TotalCount
		offset += len(page.Data)
		if len(page.Data) == 0 || offset >= total {
			break
		}
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	return json.Marshal(map[string]any{
		"offset":     0,
		"limit":      len(items),
		"count":      len(items),
		"totalCount": total,
		"data":       items,
	})
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
