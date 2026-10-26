package spec

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed openapi.json
var raw []byte

// Raw returns the embedded OpenAPI document.
func Raw() []byte { return raw }

var (
	docOnce sync.Once
	doc     map[string]any
	docErr  error
)

// Doc parses the embedded spec on first use. Running commands never needs
// it; only help and schema lookups do.
func Doc() (map[string]any, error) {
	docOnce.Do(func() {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		docErr = d.Decode(&doc)
	})
	return doc, docErr
}

const refPrefix = "#/components/schemas/"

func schemas() (map[string]any, error) {
	d, err := Doc()
	if err != nil {
		return nil, err
	}
	comps, _ := d["components"].(map[string]any)
	s, _ := comps["schemas"].(map[string]any)
	return s, nil
}

// Topic returns the description of a documentation tag in the spec, such as
// "Filtering", matched ignoring case.
func Topic(name string) (string, error) {
	d, err := Doc()
	if err != nil {
		return "", err
	}
	tags, _ := d["tags"].([]any)
	for _, t := range tags {
		tm, _ := t.(map[string]any)
		if n, _ := tm["name"].(string); strings.EqualFold(n, name) {
			desc, _ := tm["description"].(string)
			return desc, nil
		}
	}
	return "", fmt.Errorf("topic %q is not in the spec for %s", name, SpecVersion)
}

// SchemaNames lists all component schema names.
func SchemaNames() ([]string, error) {
	s, err := schemas()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(s))
	for n := range s {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// LookupSchema finds a component schema by name, ignoring case, spaces,
// dashes and underscores ("firewall-policy" matches "Firewall policy").
func LookupSchema(name string) (string, map[string]any, error) {
	s, err := schemas()
	if err != nil {
		return "", nil, err
	}
	if v, ok := s[name].(map[string]any); ok {
		return name, v, nil
	}
	want := squash(name)
	for n, v := range s {
		if squash(n) == want {
			m, _ := v.(map[string]any)
			return n, m, nil
		}
	}
	return "", nil, fmt.Errorf("schema %q not found (try `unifi schema --list`)", name)
}

func squash(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_':
			return -1
		}
		return r
	}, strings.ToLower(s))
}

// RequestSchema returns the JSON request body schema for an op.
func RequestSchema(op *Op) (map[string]any, error) {
	d, err := Doc()
	if err != nil {
		return nil, err
	}
	paths, _ := d["paths"].(map[string]any)
	item, _ := paths[op.Path].(map[string]any)
	o, _ := item[strings.ToLower(op.Method)].(map[string]any)
	rb, _ := o["requestBody"].(map[string]any)
	content, _ := rb["content"].(map[string]any)
	mt, _ := content["application/json"].(map[string]any)
	sch, _ := mt["schema"].(map[string]any)
	return sch, nil
}

// Expand renders a schema for reading: $refs are inlined to the given depth,
// allOf members are merged into one object, and discriminated (polymorphic)
// schemas list each concrete variant under "x-variants", keyed by the
// discriminator value(s), with the discriminator property pinned via
// "const". Refs beyond the depth, or that would recurse, stay as
// {"$ref": "<name>"} for lookup with `unifi schema <name>`.
func Expand(sch map[string]any, depth int) (any, error) {
	s, err := schemas()
	if err != nil {
		return nil, err
	}
	e := expander{all: s, stack: map[string]bool{}}
	return e.node(sch, depth), nil
}

type expander struct {
	all   map[string]any
	stack map[string]bool
}

func refName(m map[string]any) string {
	if ref, ok := m["$ref"].(string); ok && strings.HasPrefix(ref, refPrefix) {
		return strings.TrimPrefix(ref, refPrefix)
	}
	return ""
}

func (e expander) node(v any, depth int) any {
	switch t := v.(type) {
	case map[string]any:
		return e.schema(t, depth)
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = e.node(val, depth)
		}
		return out
	}
	return v
}

func (e expander) schema(m map[string]any, depth int) any {
	if name := refName(m); name != "" {
		target, _ := e.all[name].(map[string]any)
		if depth <= 0 || e.stack[name] || target == nil {
			return map[string]any{"$ref": name}
		}
		e.stack[name] = true
		defer delete(e.stack, name)
		out, ok := e.schema(target, depth-1).(map[string]any)
		if !ok {
			return out
		}
		out["x-schema"] = name
		if d, ok := m["description"]; ok {
			out["description"] = d
		}
		return out
	}
	m = e.mergeAllOf(m)

	out := make(map[string]any, len(m))
	for k, val := range m {
		switch k {
		case "example", "discriminator":
		case "properties":
			props, _ := val.(map[string]any)
			po := make(map[string]any, len(props))
			for pk, pv := range props {
				po[pk] = e.node(pv, depth)
			}
			out[k] = po
		default:
			out[k] = e.node(val, depth)
		}
	}

	disc, _ := m["discriminator"].(map[string]any)
	prop, _ := disc["propertyName"].(string)
	mapping, _ := disc["mapping"].(map[string]any)
	if prop == "" || len(mapping) == 0 {
		return out
	}
	// Group discriminator values by target so shared variants appear once.
	byTarget := map[string][]string{}
	for val, ref := range mapping {
		if s, ok := ref.(string); ok {
			name := strings.TrimPrefix(s, refPrefix)
			byTarget[name] = append(byTarget[name], val)
		}
	}
	variants := make(map[string]any, len(byTarget))
	for name, vals := range byTarget {
		sort.Strings(vals)
		key := strings.Join(vals, "|")
		if depth <= 0 || e.stack[name] {
			variants[key] = map[string]any{"$ref": name}
			continue
		}
		v, ok := e.schema(map[string]any{"$ref": refPrefix + name}, depth).(map[string]any)
		if !ok {
			continue
		}
		if props, ok := v["properties"].(map[string]any); ok {
			pinned := map[string]any{"type": "string"}
			if len(vals) == 1 {
				pinned["const"] = vals[0]
			} else {
				pinned["enum"] = vals
			}
			props[prop] = pinned
		}
		variants[key] = v
	}
	out["x-discriminator"] = prop
	out["x-variants"] = variants
	return out
}

// mergeAllOf folds allOf members into one schema: properties and required
// are unioned. Members are the polymorphic parent of a variant, so their
// discriminators are dropped to avoid listing the variants again.
func (e expander) mergeAllOf(m map[string]any) map[string]any {
	members, ok := m["allOf"].([]any)
	if !ok {
		return m
	}
	out := map[string]any{}
	props := map[string]any{}
	var required []any
	seenReq := map[string]bool{}
	addReq := func(r any) {
		list, _ := r.([]any)
		for _, x := range list {
			if s, ok := x.(string); ok && !seenReq[s] {
				seenReq[s] = true
				required = append(required, s)
			}
		}
	}
	absorb := func(s map[string]any) {
		for k, v := range s {
			switch k {
			case "properties":
				pm, _ := v.(map[string]any)
				for pk, pv := range pm {
					props[pk] = pv
				}
			case "required":
				addReq(v)
			case "discriminator", "allOf":
			default:
				if _, set := out[k]; !set {
					out[k] = v
				}
			}
		}
	}
	for _, mem := range members {
		mm, _ := mem.(map[string]any)
		if name := refName(mm); name != "" {
			target, _ := e.all[name].(map[string]any)
			mm = e.mergeAllOf(target)
		}
		absorb(mm)
	}
	rest := make(map[string]any, len(m))
	for k, v := range m {
		if k != "allOf" {
			rest[k] = v
		}
	}
	absorb(rest)
	if len(props) > 0 {
		out["properties"] = props
	}
	if len(required) > 0 {
		out["required"] = required
	}
	if d, ok := m["discriminator"]; ok {
		out["discriminator"] = d
	}
	return out
}
