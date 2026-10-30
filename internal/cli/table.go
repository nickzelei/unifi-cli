package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
)

// defaultColumns are tried, in order, when --fields isn't given. A column is
// shown if any row has a scalar value for it.
var defaultColumns = []string{"name", "type", "state", "model", "macAddress", "ipAddress", "vlanId", "enabled", "action", "id"}

// maxDefaultColumns caps the fallback when no default column matches.
const maxDefaultColumns = 6

// maxCell truncates nested values rendered as JSON.
const maxCell = 60

// emitTable renders a response for humans: pages and arrays as rows, a
// single object as field/value pairs. Paging that left items out is noted
// on stderr. Bodies that aren't JSON objects or arrays print as-is.
func emitTable(io IO, body []byte, fields []string) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		_, err := fmt.Fprintf(io.Out, "%s\n", body)
		return err
	}
	tw := tabwriter.NewWriter(io.Out, 0, 0, 2, ' ', 0)
	switch t := v.(type) {
	case []any:
		writeRows(tw, t, fields)
	case map[string]any:
		data, isPage := t["data"].([]any)
		if _, paged := t["totalCount"]; isPage && paged {
			writeRows(tw, data, fields)
			if err := tw.Flush(); err != nil {
				return err
			}
			total := cell(t["totalCount"])
			if fmt.Sprint(len(data)) != total {
				fmt.Fprintf(io.Err, "unifi: showing %d of %s (use --all, or --offset/--limit)\n", len(data), total)
			}
			return nil
		}
		writeObject(tw, t, fields)
	default:
		fmt.Fprintln(tw, cell(v))
	}
	return tw.Flush()
}

func writeRows(tw *tabwriter.Writer, rows []any, fields []string) {
	if len(rows) == 0 {
		fmt.Fprintln(tw, "(no items)")
		return
	}
	cols := fields
	if len(cols) == 0 {
		cols = pickColumns(rows)
	}
	fmt.Fprintln(tw, strings.Join(cols, "\t"))
	for _, r := range rows {
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = cell(lookup(r, strings.Split(c, ".")))
		}
		fmt.Fprintln(tw, strings.Join(vals, "\t"))
	}
}

// writeObject prints one object as FIELD/VALUE lines, in --fields order or
// sorted by key.
func writeObject(tw *tabwriter.Writer, obj map[string]any, fields []string) {
	keys := fields
	if len(keys) == 0 {
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	fmt.Fprintln(tw, "FIELD\tVALUE")
	for _, k := range keys {
		fmt.Fprintf(tw, "%s\t%s\n", k, cell(lookup(obj, strings.Split(k, "."))))
	}
}

// pickColumns chooses default columns from those the rows actually have.
func pickColumns(rows []any) []string {
	has := map[string]bool{}
	var order []string
	for _, r := range rows {
		m, _ := r.(map[string]any)
		for k, v := range m {
			if !has[k] && isScalar(v) {
				has[k] = true
				order = append(order, k)
			}
		}
	}
	var cols []string
	for _, c := range defaultColumns {
		if has[c] {
			cols = append(cols, c)
		}
	}
	if len(cols) > 0 {
		return cols
	}
	sort.Strings(order)
	if len(order) > maxDefaultColumns {
		order = order[:maxDefaultColumns]
	}
	if len(order) == 0 {
		return []string{"value"}
	}
	return order
}

func isScalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

// lookup follows a dotted path; arrays along it are mapped per element.
func lookup(v any, path []string) any {
	if len(path) == 0 {
		return v
	}
	switch t := v.(type) {
	case map[string]any:
		next, ok := t[path[0]]
		if !ok {
			return nil
		}
		return lookup(next, path[1:])
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = lookup(e, path)
		}
		return out
	}
	return nil
}

func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case string:
		if t == "" {
			return "-"
		}
		return strings.NewReplacer("\t", " ", "\n", " ").Replace(t)
	case json.Number:
		return t.String()
	case bool:
		return fmt.Sprint(t)
	case []any:
		if allScalar(t) {
			parts := make([]string, len(t))
			for i, e := range t {
				parts[i] = cell(e)
			}
			return strings.Join(parts, ",")
		}
	}
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > maxCell {
		s = s[:maxCell-1] + "…"
	}
	return s
}

func allScalar(a []any) bool {
	for _, e := range a {
		if !isScalar(e) {
			return false
		}
	}
	return true
}
