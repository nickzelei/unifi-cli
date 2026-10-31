package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/nickzelei/unifi-cli/internal/spec"
	"github.com/nickzelei/unifi-cli/internal/unifi"
)

// matchFields are the item fields a non-UUID ID argument is matched against,
// when the item has them.
var matchFields = []string{"name", "macAddress", "ipAddress"}

// lookupOp returns the list op that can resolve arg a of op, i.e. the paged
// GET at op's path cut just before "/{a}". Every UUID path arg in the spec
// has one, e.g. devices get <device-id> -> GET /v1/sites/{siteId}/devices.
func lookupOp(op *spec.Op, a spec.Param) *spec.Op {
	i := strings.Index(op.Path, "/{"+a.Name+"}")
	if a.Format != "uuid" || i < 0 {
		return nil
	}
	prefix := op.Path[:i]
	for j := range spec.Ops {
		if l := &spec.Ops[j]; l.Paged && l.Method == http.MethodGet && l.Path == prefix {
			return l
		}
	}
	return nil
}

// resolveID turns a name, MAC or IP into the UUID of the one matching item
// in the list at listPath (already filled in). UUIDs never reach here.
func resolveID(ctx context.Context, c *unifi.Client, a spec.Param, listPath, sel string) (string, error) {
	b, err := fetchAll(ctx, c, listPath, nil)
	if err != nil {
		return "", fmt.Errorf("resolve <%s> %q: %w", a.Flag, sel, err)
	}
	var page struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		return "", fmt.Errorf("resolve <%s>: decode list: %w", a.Flag, err)
	}
	var hits []map[string]any
	for _, item := range page.Data {
		if matches(item, sel) {
			hits = append(hits, item)
		}
	}
	noun := strings.TrimSuffix(a.Flag, "-id")
	switch len(hits) {
	case 1:
		id, _ := hits[0]["id"].(string)
		if !unifi.IsUUID(id) {
			return "", fmt.Errorf("resolve <%s> %q: matched item has no UUID id", a.Flag, sel)
		}
		return id, nil
	case 0:
		return "", fmt.Errorf("no %s matches %q by %s (%d checked); pass a UUID or check the list output", noun, sel, fieldsOf(page.Data), len(page.Data))
	}
	desc := make([]string, len(hits))
	for i, h := range hits {
		desc[i] = describe(h)
	}
	return "", fmt.Errorf("%q matches %d %ss; pass a UUID instead: %s", sel, len(hits), noun, strings.Join(desc, "; "))
}

// matches compares sel with an item's name (ignoring case), MAC (ignoring
// case and separators) or IP.
func matches(item map[string]any, sel string) bool {
	for _, f := range matchFields {
		v, _ := item[f].(string)
		if v == "" {
			continue
		}
		switch f {
		case "macAddress":
			if normMAC(v) == normMAC(sel) {
				return true
			}
		case "ipAddress":
			if v == sel {
				return true
			}
		default:
			if strings.EqualFold(v, sel) {
				return true
			}
		}
	}
	return false
}

func normMAC(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ':', '-', '.':
			return -1
		}
		return r
	}, strings.ToLower(s))
}

// fieldsOf lists which match fields the items have, for error messages.
func fieldsOf(items []map[string]any) string {
	var have []string
	for _, f := range matchFields {
		for _, it := range items {
			if _, ok := it[f]; ok {
				have = append(have, f)
				break
			}
		}
	}
	if len(have) == 0 {
		return "name"
	}
	return strings.Join(have, "/")
}

func describe(item map[string]any) string {
	id, _ := item["id"].(string)
	var extra []string
	for _, f := range matchFields {
		if v, ok := item[f].(string); ok && v != "" {
			extra = append(extra, v)
		}
	}
	if len(extra) == 0 {
		return id
	}
	return fmt.Sprintf("%s (%s)", id, strings.Join(extra, ", "))
}
