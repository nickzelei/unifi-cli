package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// emit writes a JSON response body. Bodies pass through untouched unless
// they need projecting or indenting. Empty bodies print nothing.
func emit(io IO, body []byte, pretty bool, fields []string) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	pretty = pretty || io.OutTTY
	if len(fields) == 0 && !pretty {
		_, err := fmt.Fprintf(io.Out, "%s\n", body)
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		// Not JSON: print as-is.
		_, err := fmt.Fprintf(io.Out, "%s\n", body)
		return err
	}
	if len(fields) > 0 {
		v = project(v, fields)
	}
	enc := json.NewEncoder(io.Out)
	enc.SetEscapeHTML(false)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

// project keeps only the given dotted field paths. Arrays are projected per
// element, and a page object ({data: [...], ...}) keeps its paging metadata.
func project(v any, fields []string) any {
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = project(e, fields)
		}
		return out
	case map[string]any:
		if data, ok := t["data"].([]any); ok {
			if _, paged := t["totalCount"]; paged {
				out := make(map[string]any, len(t))
				for k, val := range t {
					out[k] = val
				}
				out["data"] = project(data, fields)
				return out
			}
		}
		var out any = map[string]any{}
		for _, f := range fields {
			if val, ok := pick(t, strings.Split(f, ".")); ok {
				out = merge(out, val)
			}
		}
		return out
	}
	return v
}

// pick extracts one dotted path, keeping the surrounding structure: arrays
// along the path stay arrays, with the rest of the path picked per element.
func pick(v any, path []string) (any, bool) {
	if len(path) == 0 {
		return v, true
	}
	switch t := v.(type) {
	case map[string]any:
		next, ok := t[path[0]]
		if !ok {
			return nil, false
		}
		sub, ok := pick(next, path[1:])
		if !ok {
			return nil, false
		}
		return map[string]any{path[0]: sub}, true
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			sub, ok := pick(e, path)
			if !ok {
				sub = map[string]any{}
			}
			out[i] = sub
		}
		return out, true
	}
	return nil, false
}

// merge deep-merges two picked values: maps by key, equal-length arrays by
// element.
func merge(a, b any) any {
	switch at := a.(type) {
	case map[string]any:
		bt, ok := b.(map[string]any)
		if !ok {
			return b
		}
		for k, bv := range bt {
			if av, ok := at[k]; ok {
				at[k] = merge(av, bv)
			} else {
				at[k] = bv
			}
		}
		return at
	case []any:
		bt, ok := b.([]any)
		if !ok || len(bt) != len(at) {
			return b
		}
		for i := range at {
			at[i] = merge(at[i], bt[i])
		}
		return at
	}
	return b
}
