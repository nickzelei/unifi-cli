package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ConsoleVersion returns the Network application version from /v1/info.
func (c *Client) ConsoleVersion(ctx context.Context) (string, error) {
	b, err := c.Do(ctx, "GET", "/v1/info", nil, nil)
	if err != nil {
		return "", err
	}
	var info struct {
		ApplicationVersion string `json:"applicationVersion"`
	}
	if err := json.Unmarshal(b, &info); err != nil {
		return "", fmt.Errorf("decode /v1/info: %w", err)
	}
	if info.ApplicationVersion == "" {
		return "", fmt.Errorf("/v1/info has no applicationVersion")
	}
	return info.ApplicationVersion, nil
}

// CompareVersions compares dotted numeric versions such as "10.6.109",
// returning -1, 0 or 1. Each part compares by its leading digits, so a
// suffix like "-beta" is ignored; missing parts count as 0.
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		x, y := part(pa, i), part(pb, i)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

func part(p []string, i int) int {
	if i >= len(p) {
		return 0
	}
	digits := strings.IndexFunc(p[i], func(r rune) bool { return r < '0' || r > '9' })
	if digits < 0 {
		digits = len(p[i])
	}
	n, _ := strconv.Atoi(p[i][:digits])
	return n
}
