package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s looks like a UUID.
func IsUUID(s string) bool { return uuidRE.MatchString(s) }

// Site is a local site as returned by GET /v1/sites.
type Site struct {
	ID                string `json:"id"`
	InternalReference string `json:"internalReference"`
	Name              string `json:"name"`
}

// ListSites returns all local sites.
func (c *Client) ListSites(ctx context.Context) ([]Site, error) {
	b, err := c.Do(ctx, "GET", "/v1/sites", url.Values{"limit": {"200"}}, nil)
	if err != nil {
		return nil, err
	}
	var page struct {
		Data []Site `json:"data"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		return nil, fmt.Errorf("decode sites: %w", err)
	}
	return page.Data, nil
}

// ResolveSite turns a site selector (UUID, internal reference, or name) into
// a site ID. UUIDs are returned without a request. An empty selector picks
// the only site, if there is exactly one.
func (c *Client) ResolveSite(ctx context.Context, sel string) (string, error) {
	if IsUUID(sel) {
		return sel, nil
	}
	sites, err := c.ListSites(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve site: %w", err)
	}
	return MatchSite(sites, sel)
}

// MatchSite picks a site by selector from a list.
func MatchSite(sites []Site, sel string) (string, error) {
	if sel == "" {
		if len(sites) == 1 {
			return sites[0].ID, nil
		}
		return "", fmt.Errorf("multiple sites found; pass --site (one of: %s)", describeSites(sites))
	}
	for _, s := range sites {
		if strings.EqualFold(s.ID, sel) || strings.EqualFold(s.InternalReference, sel) || strings.EqualFold(s.Name, sel) {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("site %q not found (have: %s)", sel, describeSites(sites))
}

func describeSites(sites []Site) string {
	parts := make([]string, len(sites))
	for i, s := range sites {
		parts[i] = fmt.Sprintf("%s [%s] %s", s.InternalReference, s.Name, s.ID)
	}
	return strings.Join(parts, "; ")
}
