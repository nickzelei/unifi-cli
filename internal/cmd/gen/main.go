// Command gen maintains the embedded OpenAPI spec and the generated
// operation table.
//
//	gen fetch [-url URL] [-profile NAME]   download the spec into internal/spec/openapi.json
//	gen ops                                write internal/spec/ops_gen.go from the spec
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nickzelei/unifi-cli/internal/unifi"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gen fetch|ops")
		os.Exit(2)
	}
	root, err := moduleRoot()
	if err == nil {
		switch os.Args[1] {
		case "fetch":
			err = fetch(root, os.Args[2:])
		case "ops":
			err = genOps(root)
		default:
			err = fmt.Errorf("unknown subcommand %q", os.Args[1])
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

func specPath(root string) string { return filepath.Join(root, "internal", "spec", "openapi.json") }

// fetch downloads the spec from the configured console (default) or a URL,
// and rewrites it with sorted keys and indentation so updates diff cleanly.
func fetch(root string, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	src := fs.String("url", "", "spec URL (e.g. https://developer.ui.com/network/v10.6.106/openapi.json); default: the console from your unifi config")
	profile := fs.String("profile", "", "unifi config profile to fetch from")
	_ = fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var body []byte
	var err error
	if *src != "" {
		body, err = httpGet(ctx, *src)
	} else {
		body, err = fetchFromConsole(ctx, *profile)
	}
	if err != nil {
		return err
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("spec is not JSON: %w", err)
	}
	if _, ok := doc["openapi"]; !ok {
		return errors.New("response is not an OpenAPI document")
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := os.WriteFile(specPath(root), out.Bytes(), 0o644); err != nil {
		return err
	}
	info, _ := doc["info"].(map[string]any)
	fmt.Printf("wrote %s (%v %v)\n", specPath(root), info["title"], info["version"])
	return nil
}

func httpGet(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func fetchFromConsole(ctx context.Context, profile string) ([]byte, error) {
	cfg, err := unifi.Load(profile)
	if err != nil {
		return nil, err
	}
	c, err := unifi.NewClient(cfg.Profile, 60*time.Second)
	if err != nil {
		return nil, err
	}
	origin, err := cfg.Profile.Origin()
	if err != nil {
		return nil, err
	}
	return c.DoURL(ctx, "GET", origin+"/proxy/network/api-docs/integration.json", nil)
}

// override customizes the command for operations the default naming rules
// can't name well. Any operation with path segments after its first ID
// parameter, or a POST on an item, must have one, so new endpoints get a
// deliberate name instead of a guessed one.
type override struct {
	verb        string
	destructive bool
}

var overrides = map[string]override{
	"adoptDevice":                      {verb: "adopt"},
	"removeDevice":                     {verb: "remove", destructive: true},
	"executeAdoptedDeviceAction":       {verb: "action", destructive: true},
	"executePortAction":                {verb: "port-action", destructive: true},
	"getAdoptedDeviceLatestStatistics": {verb: "statistics"},
	"executeConnectedClientAction":     {verb: "action", destructive: true},
	"getNetworkReferences":             {verb: "references"},
	"deleteVouchers":                   {verb: "delete-matching", destructive: true},
}

type genParam struct {
	Name, Flag, In, Type, Format, Default, Description string
	Required                                           bool
}

type genOp struct {
	ID, Method, Path, Summary, Description, Tag, Response string
	Command                                               []string
	SiteScoped, Body, BodyRequired, Paged, Destructive    bool
	Args, Flags                                           []genParam
}

func genOps(root string) error {
	b, err := os.ReadFile(specPath(root))
	if err != nil {
		return err
	}
	var doc struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}

	var ops []genOp
	used := map[string]string{}
	seenOverride := map[string]bool{}
	for path, item := range doc.Paths {
		var shared []rawParam
		if p, ok := item["parameters"]; ok {
			if err := json.Unmarshal(p, &shared); err != nil {
				return err
			}
		}
		for method, raw := range item {
			m := strings.ToUpper(method)
			switch m {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				continue
			}
			var o rawOp
			if err := json.Unmarshal(raw, &o); err != nil {
				return fmt.Errorf("%s %s: %w", m, path, err)
			}
			op, err := buildOp(m, path, o, shared)
			if err != nil {
				return err
			}
			if _, ok := overrides[op.ID]; ok {
				seenOverride[op.ID] = true
			}
			name := strings.Join(op.Command, " ")
			if prev, ok := used[name]; ok {
				return fmt.Errorf("command %q is used by both %s and %s: add an override", name, prev, op.ID)
			}
			used[name] = op.ID
			ops = append(ops, op)
		}
	}
	for id := range overrides {
		if !seenOverride[id] {
			return fmt.Errorf("override for %q matches no operation: remove it", id)
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		return strings.Join(ops[i].Command, " ") < strings.Join(ops[j].Command, " ")
	})

	var buf bytes.Buffer
	buf.WriteString("// Code generated by internal/cmd/gen from openapi.json. DO NOT EDIT.\n\npackage spec\n\n")
	fmt.Fprintf(&buf, "// SpecVersion is the UniFi Network API version of the embedded spec.\nconst SpecVersion = %q\n\n", doc.Info.Version)
	buf.WriteString("// Ops lists every API operation, sorted by command.\nvar Ops = []Op{\n")
	for _, op := range ops {
		writeOp(&buf, op)
	}
	buf.WriteString("}\n")
	src, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("format: %w", err)
	}
	out := filepath.Join(root, "internal", "spec", "ops_gen.go")
	if err := os.WriteFile(out, src, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d operations, spec %s)\n", out, len(ops), doc.Info.Version)
	return nil
}

type rawSchema struct {
	Ref     string          `json:"$ref"`
	Type    string          `json:"type"`
	Format  string          `json:"format"`
	Default json.RawMessage `json:"default"`
	Maximum json.RawMessage `json:"maximum"`
	Enum    []any           `json:"enum"`
}

type rawParam struct {
	Name        string    `json:"name"`
	In          string    `json:"in"`
	Required    bool      `json:"required"`
	Description string    `json:"description"`
	Schema      rawSchema `json:"schema"`
}

type rawOp struct {
	OperationID string     `json:"operationId"`
	Summary     string     `json:"summary"`
	Description string     `json:"description"`
	Tags        []string   `json:"tags"`
	Parameters  []rawParam `json:"parameters"`
	RequestBody *struct {
		Required bool                                  `json:"required"`
		Content  map[string]struct{ Schema rawSchema } `json:"content"`
	} `json:"requestBody"`
	Responses map[string]struct {
		Content map[string]struct{ Schema rawSchema } `json:"content"`
	} `json:"responses"`
}

func buildOp(method, path string, o rawOp, shared []rawParam) (genOp, error) {
	if o.OperationID == "" {
		return genOp{}, fmt.Errorf("%s %s has no operationId", method, path)
	}
	op := genOp{
		ID:          o.OperationID,
		Method:      method,
		Path:        path,
		Summary:     o.Summary,
		Description: cleanDescription(o.Description),
		Body:        o.RequestBody != nil,
	}
	if len(o.Tags) > 0 {
		op.Tag = o.Tags[0]
	}
	if o.RequestBody != nil {
		op.BodyRequired = o.RequestBody.Required
	}
	for _, code := range []string{"200", "201"} {
		if r, ok := o.Responses[code]; ok {
			if ref := r.Content["application/json"].Schema.Ref; ref != "" {
				op.Response = strings.TrimPrefix(ref, "#/components/schemas/")
			}
			break
		}
	}

	// Command words come from the path: drop "/v1" and "sites/{siteId}",
	// keep the static segments before the first ID parameter as the group.
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) > 0 && segs[0] == "v1" {
		segs = segs[1:]
	}
	if len(segs) >= 2 && segs[0] == "sites" && segs[1] == "{siteId}" {
		op.SiteScoped = true
		segs = segs[2:]
	}
	firstParam := -1
	for i, s := range segs {
		if strings.HasPrefix(s, "{") {
			firstParam = i
			break
		}
	}
	group := segs
	var rest []string
	if firstParam >= 0 {
		group, rest = segs[:firstParam], segs[firstParam+1:]
	}
	if len(group) == 0 {
		return genOp{}, fmt.Errorf("%s %s: cannot derive a command group", method, path)
	}

	params := append(append([]rawParam{}, shared...), o.Parameters...)
	for _, p := range params {
		if p.Name == "offset" {
			op.Paged = true
		}
	}

	ov, hasOverride := overrides[op.ID]
	verb := ov.verb
	if !hasOverride {
		if len(rest) > 0 || (firstParam >= 0 && method == "POST") {
			return genOp{}, fmt.Errorf("%s %s (%s) needs a command name: add it to overrides in internal/cmd/gen", method, path, op.ID)
		}
		verb = defaultVerb(method, firstParam >= 0, op.Paged)
	}
	op.Command = append(append([]string{}, group...), verb)
	op.Destructive = method == "DELETE" || ov.destructive

	// Path params become positional args in path order.
	byName := map[string]rawParam{}
	for _, p := range params {
		byName[p.In+":"+p.Name] = p
	}
	for _, s := range segs {
		if !strings.HasPrefix(s, "{") {
			continue
		}
		name := strings.Trim(s, "{}")
		p, ok := byName["path:"+name]
		if !ok {
			p = rawParam{Name: name, In: "path", Required: true, Schema: rawSchema{Type: "string"}}
		}
		op.Args = append(op.Args, toParam(p))
	}
	for _, p := range params {
		if p.In == "query" {
			op.Flags = append(op.Flags, toParam(p))
		}
	}
	return op, nil
}

func defaultVerb(method string, item, paged bool) string {
	switch method {
	case "GET":
		if !item && paged {
			return "list"
		}
		return "get"
	case "POST":
		return "create"
	case "PUT":
		return "update"
	case "PATCH":
		return "patch"
	}
	return "delete"
}

func toParam(p rawParam) genParam {
	gp := genParam{
		Name:        p.Name,
		Flag:        kebab(p.Name),
		In:          p.In,
		Required:    p.Required,
		Type:        p.Schema.Type,
		Format:      p.Schema.Format,
		Description: p.Description,
	}
	if gp.Type == "" {
		gp.Type = "string"
	}
	if len(p.Schema.Default) > 0 {
		gp.Default = strings.Trim(string(p.Schema.Default), `"`)
	}
	var extra []string
	if len(p.Schema.Maximum) > 0 {
		extra = append(extra, "max "+string(p.Schema.Maximum))
	}
	if len(p.Schema.Enum) > 0 {
		vals := make([]string, len(p.Schema.Enum))
		for i, v := range p.Schema.Enum {
			vals[i] = fmt.Sprint(v)
		}
		extra = append(extra, "one of: "+strings.Join(vals, ", "))
	}
	if len(extra) > 0 {
		gp.Description = strings.TrimSpace(gp.Description + " (" + strings.Join(extra, "; ") + ")")
	}
	return gp
}

var upperRE = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func kebab(s string) string { return strings.ToLower(upperRE.ReplaceAllString(s, "$1-$2")) }

var htmlTagRE = regexp.MustCompile(`</?(details|summary)>`)

// cleanDescription strips the collapsible HTML wrappers the spec uses around
// filterable-property tables.
func cleanDescription(s string) string {
	s = strings.ReplaceAll(s, "<summary>Filterable properties (click to expand)</summary>", "Filterable properties:")
	s = htmlTagRE.ReplaceAllString(s, "")
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func writeOp(w *bytes.Buffer, op genOp) {
	fmt.Fprintf(w, "{\nID: %q,\nCommand: %#v,\nMethod: %q,\nPath: %q,\nSummary: %q,\n", op.ID, op.Command, op.Method, op.Path, op.Summary)
	if op.Description != "" {
		fmt.Fprintf(w, "Description: %q,\n", op.Description)
	}
	if op.Tag != "" {
		fmt.Fprintf(w, "Tag: %q,\n", op.Tag)
	}
	for _, f := range []struct {
		name string
		v    bool
	}{{"SiteScoped", op.SiteScoped}, {"Body", op.Body}, {"BodyRequired", op.BodyRequired}, {"Paged", op.Paged}, {"Destructive", op.Destructive}} {
		if f.v {
			fmt.Fprintf(w, "%s: true,\n", f.name)
		}
	}
	if op.Response != "" {
		fmt.Fprintf(w, "Response: %q,\n", op.Response)
	}
	writeParams(w, "Args", op.Args)
	writeParams(w, "Flags", op.Flags)
	w.WriteString("},\n")
}

func writeParams(w *bytes.Buffer, field string, ps []genParam) {
	if len(ps) == 0 {
		return
	}
	fmt.Fprintf(w, "%s: []Param{\n", field)
	for _, p := range ps {
		fmt.Fprintf(w, "{Name: %q, Flag: %q, In: %q, Type: %q", p.Name, p.Flag, p.In, p.Type)
		if p.Required {
			w.WriteString(", Required: true")
		}
		if p.Format != "" {
			fmt.Fprintf(w, ", Format: %q", p.Format)
		}
		if p.Default != "" {
			fmt.Fprintf(w, ", Default: %q", p.Default)
		}
		if p.Description != "" {
			fmt.Fprintf(w, ", Description: %q", p.Description)
		}
		w.WriteString("},\n")
	}
	w.WriteString("},\n")
}
