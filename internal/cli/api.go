package cli

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"strings"

	"github.com/nickzelei/unifi-cli/internal/unifi"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// apiCmd sends a raw request to an integration API path. It covers endpoints
// newer than the embedded spec.
func apiCmd(ctx context.Context, args []string, io IO) error {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	fs.Usage = func() {
		fmt.Fprint(io.Out, `Send a raw request to the integration API.

Usage:
  unifi api <METHOD> <path> [--query k=v]... [--data <json>] [flags]

The path is relative to the integration base (…/proxy/network/integration),
e.g. /v1/sites/{siteId}/wans. {siteId} is replaced with the resolved --site.
Methods other than GET require --yes, and are refused on a read-only profile.

Flags:
  --query k=v      query parameter (repeatable)
  --data <json>    request body: inline JSON, @file, or - for stdin
  --yes            confirm a non-GET request
  global           --site --profile --fields --pretty -o/--output --dry-run --timeout
`)
	}
	var g globals
	g.register(fs)
	var query multiFlag
	fs.Var(&query, "query", "query parameter k=v (repeatable)")
	data := fs.String("data", "", "request body")
	pos, err := parseInterleaved(fs, args)
	if err == nil {
		err = g.checkOutput()
	}
	if err != nil {
		return flagErr(err, "api")
	}
	if len(pos) != 2 {
		return usagef("usage: unifi api <METHOD> <path> (see `unifi api --help`)")
	}
	method, path := strings.ToUpper(pos[0]), pos[1]
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	q := url.Values{}
	for _, kv := range query {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return usagef("--query %q: want key=value", kv)
		}
		q.Add(k, v)
	}
	body, err := readData(*data, io.In)
	if err != nil {
		return err
	}
	cfg, err := unifi.Load(g.profile)
	if err != nil {
		return err
	}
	if err := checkReadOnly(cfg, method, g.dryRun); err != nil {
		return err
	}
	if method != "GET" && !g.yes && !g.dryRun {
		return usagef("%s requests change state; re-run with --yes to confirm", method)
	}
	c, err := unifi.NewClient(cfg.Profile, g.timeout)
	if err != nil {
		return err
	}
	if strings.Contains(path, "{siteId}") {
		site := g.site
		if site == "" {
			site = cfg.Profile.Site
		}
		id, err := c.ResolveSite(ctx, site)
		if err != nil {
			return err
		}
		path = strings.ReplaceAll(path, "{siteId}", id)
	}
	if g.dryRun {
		return printDryRun(io, method, c.URL(path, q), body)
	}
	out, err := c.Do(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	return g.emit(io, out)
}
