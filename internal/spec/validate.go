package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Validation is the result of checking a request body against an op's
// schema. Errors are definite violations; Warnings are properties the
// embedded spec doesn't know, which newer firmware may still accept.
type Validation struct {
	Errors   []string
	Warnings []string
}

// Validate checks a JSON request body against op's request schema. It covers
// the JSON Schema subset the UniFi spec uses: type, properties, required,
// allOf, discriminator, enum, items, min/max(Length|Items), uniqueItems and
// the uuid/date/int32 formats. It leans permissive where the spec is loose:
// untyped properties accept anything and optional properties accept null.
func Validate(op *Op, body []byte) (*Validation, error) {
	sch, err := RequestSchema(op)
	if err != nil || sch == nil {
		return &Validation{}, err
	}
	all, err := schemas()
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	val := &validator{e: expander{all: all, stack: map[string]bool{}}, out: &Validation{}}
	val.readOnly = val.responseProps(op)
	val.check("$", v, sch, 0, "")
	sort.Strings(val.out.Errors)
	sort.Strings(val.out.Warnings)
	return val.out, nil
}

type validator struct {
	e   expander
	out *Validation
	// readOnly holds top-level properties of the matching GET response
	// (id, metadata, ...). They come back when a body is built from `get`
	// output, so they aren't warned about.
	readOnly map[string]bool
}

// responseProps returns the top-level property names of the response of
// the GET for the same object: the GET at op's path, or for a create, at
// op's path plus "/{id}". Variants of a polymorphic response are included.
func (v *validator) responseProps(op *Op) map[string]bool {
	out := map[string]bool{}
	for i := range Ops {
		g := &Ops[i]
		if g.Method != "GET" || g.Response == "" {
			continue
		}
		rest, ok := strings.CutPrefix(g.Path, op.Path)
		if !ok || !(rest == "" || strings.HasPrefix(rest, "/{") && strings.Count(rest, "/") == 1) {
			continue
		}
		s := v.resolve(map[string]any{"$ref": refPrefix + g.Response})
		v.addProps(out, s)
		disc, _ := s["discriminator"].(map[string]any)
		mapping, _ := disc["mapping"].(map[string]any)
		for _, ref := range mapping {
			if r, ok := ref.(string); ok {
				v.addProps(out, v.resolve(map[string]any{"$ref": r}))
			}
		}
	}
	return out
}

func (v *validator) addProps(out map[string]bool, s map[string]any) {
	props, _ := s["properties"].(map[string]any)
	for k := range props {
		out[k] = true
	}
}

func (v *validator) errf(path, format string, a ...any) {
	v.out.Errors = append(v.out.Errors, path+": "+fmt.Sprintf(format, a...))
}

const maxDepth = 64

// resolve follows a $ref and merges allOf, returning a flat schema.
func (v *validator) resolve(s map[string]any) map[string]any {
	for range maxDepth {
		name := refName(s)
		if name == "" {
			break
		}
		s, _ = v.e.all[name].(map[string]any)
	}
	if s == nil {
		return nil
	}
	return v.e.mergeAllOf(s)
}

// check validates val against raw. skip names a property already checked by
// a parent's discriminator: variants sometimes redeclare it with an enum
// that disagrees with the mapping (lowercase "tcp" vs "TCP").
func (v *validator) check(path string, val any, raw map[string]any, depth int, skip string) {
	s := v.resolve(raw)
	if s == nil || depth > maxDepth {
		return
	}
	// A polymorphic parent: validate against the variant the body names.
	if disc, ok := s["discriminator"].(map[string]any); ok {
		if obj, ok := val.(map[string]any); ok {
			if variant := v.variant(path, obj, disc); variant != nil {
				prop, _ := disc["propertyName"].(string)
				v.check(path, val, variant, depth+1, prop)
			}
			return
		}
	}

	if t, _ := s["type"].(string); t != "" && !typeOK(t, val) {
		v.errf(path, "must be %s, got %s", article(t), jsonType(val))
		return
	}
	if enum, ok := s["enum"].([]any); ok && !inEnum(val, enum) {
		v.errf(path, "must be one of %s, got %s", enumList(enum), text(val))
	}
	if c, ok := s["const"]; ok && text(c) != text(val) {
		v.errf(path, "must be %s, got %s", text(c), text(val))
	}

	switch x := val.(type) {
	case map[string]any:
		v.object(path, x, s, depth, skip)
	case []any:
		v.array(path, x, s, depth)
	case string:
		v.str(path, x, s)
	case json.Number:
		v.number(path, x, s)
	}
}

func (v *validator) variant(path string, obj map[string]any, disc map[string]any) map[string]any {
	prop, _ := disc["propertyName"].(string)
	mapping, _ := disc["mapping"].(map[string]any)
	var keys []string
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	got, present := obj[prop]
	tag, isStr := got.(string)
	if !present || !isStr {
		v.errf(path+"."+prop, "required: one of %s", strings.Join(keys, ", "))
		return nil
	}
	ref, ok := mapping[tag].(string)
	if !ok {
		v.errf(path+"."+prop, "must be one of %s, got %q", strings.Join(keys, ", "), tag)
		return nil
	}
	return map[string]any{"$ref": ref}
}

func (v *validator) object(path string, obj map[string]any, s map[string]any, depth int, skip string) {
	props, _ := s["properties"].(map[string]any)
	required := map[string]bool{}
	if req, ok := s["required"].([]any); ok {
		for _, r := range req {
			if name, ok := r.(string); ok {
				required[name] = true
				if _, ok := obj[name]; !ok {
					v.errf(path+"."+name, "required")
				}
			}
		}
	}
	if props == nil {
		return // free-form object
	}
	for k, val := range obj {
		p := path + "." + k
		ps, ok := props[k].(map[string]any)
		if k == skip {
			continue
		}
		if !ok {
			if path == "$" && v.readOnly[k] {
				continue
			}
			v.out.Warnings = append(v.out.Warnings, p+": not in the spec")
			continue
		}
		if val == nil && !required[k] {
			continue
		}
		v.check(p, val, ps, depth+1, "")
	}
}

func (v *validator) array(path string, arr []any, s map[string]any, depth int) {
	if n, ok := num(s["minItems"]); ok && float64(len(arr)) < n {
		v.errf(path, "must have at least %v item(s), got %d", n, len(arr))
	}
	if n, ok := num(s["maxItems"]); ok && float64(len(arr)) > n {
		v.errf(path, "must have at most %v item(s), got %d", n, len(arr))
	}
	if u, _ := s["uniqueItems"].(bool); u {
		seen := map[string]bool{}
		for _, it := range arr {
			b, _ := json.Marshal(it)
			if seen[string(b)] {
				v.errf(path, "items must be unique; %s repeats", b)
				break
			}
			seen[string(b)] = true
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		for i, it := range arr {
			v.check(fmt.Sprintf("%s[%d]", path, i), it, items, depth+1, "")
		}
	}
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (v *validator) str(path, x string, s map[string]any) {
	n := float64(utf8.RuneCountInString(x))
	if m, ok := num(s["minLength"]); ok && n < m {
		v.errf(path, "must be at least %v character(s)", m)
	}
	if m, ok := num(s["maxLength"]); ok && n > m {
		v.errf(path, "must be at most %v character(s)", m)
	}
	switch s["format"] {
	case "uuid":
		if !uuidRE.MatchString(x) {
			v.errf(path, "must be a UUID, got %q", x)
		}
	case "date":
		if _, err := time.Parse("2006-01-02", x); err != nil {
			v.errf(path, "must be a date (YYYY-MM-DD), got %q", x)
		}
	}
}

func (v *validator) number(path string, x json.Number, s map[string]any) {
	f, err := x.Float64()
	if err != nil {
		return
	}
	if m, ok := num(s["minimum"]); ok && f < m {
		v.errf(path, "must be >= %v, got %s", m, x)
	}
	if m, ok := num(s["maximum"]); ok && f > m {
		v.errf(path, "must be <= %v, got %s", m, x)
	}
	if s["format"] == "int32" && (f < math.MinInt32 || f > math.MaxInt32) {
		v.errf(path, "must fit in 32 bits, got %s", x)
	}
}

func typeOK(t string, val any) bool {
	switch t {
	case "object":
		_, ok := val.(map[string]any)
		return ok
	case "array":
		_, ok := val.([]any)
		return ok
	case "string":
		_, ok := val.(string)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "number":
		_, ok := val.(json.Number)
		return ok
	case "integer":
		n, ok := val.(json.Number)
		if !ok {
			return false
		}
		if _, err := n.Int64(); err == nil {
			return true
		}
		f, err := n.Float64()
		return err == nil && f == math.Trunc(f)
	}
	return true
}

func jsonType(val any) string {
	switch x := val.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return fmt.Sprintf("string %q", x)
	case bool:
		return fmt.Sprintf("boolean %v", x)
	case json.Number:
		return "number " + x.String()
	}
	return fmt.Sprintf("%T", val)
}

func article(t string) string {
	switch t {
	case "object", "array", "integer":
		return "an " + t
	}
	return "a " + t
}

// text renders a scalar for enum/const comparison. Comparing text keeps
// the spec's integer enums written as strings ("1", "2") working.
func text(val any) string {
	switch x := val.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return fmt.Sprint(x)
	case bool:
		return fmt.Sprint(x)
	case nil:
		return "null"
	}
	b, _ := json.Marshal(val)
	return string(b)
}

func inEnum(val any, enum []any) bool {
	for _, e := range enum {
		if text(e) == text(val) {
			return true
		}
	}
	return false
}

func enumList(enum []any) string {
	parts := make([]string, len(enum))
	for i, e := range enum {
		parts[i] = text(e)
	}
	return strings.Join(parts, ", ")
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	}
	return 0, false
}
