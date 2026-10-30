package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nickzelei/unifi-cli/internal/spec"
	"github.com/nickzelei/unifi-cli/internal/unifi"
)

// driftTTL is how long a console's version is cached. The drift check costs
// at most one /v1/info request per console per TTL.
const driftTTL = 24 * time.Hour

type versionCache struct {
	Version   string    `json:"version"`
	CheckedAt time.Time `json:"checked_at"`
}

// cacheDir is $XDG_CACHE_HOME/unifi, else the OS cache dir.
func cacheDir() (string, error) {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "unifi"), nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "unifi"), nil
}

func versionCachePath(base string) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(base))
	return filepath.Join(dir, "version-"+hex.EncodeToString(sum[:8])+".json"), nil
}

// checkDrift warns on stderr when the console runs a newer Network version
// than the embedded spec. The version is refreshed at most once per
// driftTTL, and the warning only prints on a refresh, so it shows up about
// once a day instead of on every command. Failures are silent: this must
// never break a command. UNIFI_NO_VERSION_CHECK=1 turns it off.
func checkDrift(ctx context.Context, c *unifi.Client, io IO) {
	if os.Getenv("UNIFI_NO_VERSION_CHECK") != "" {
		return
	}
	path, err := versionCachePath(c.Base())
	if err != nil {
		return
	}
	var vc versionCache
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &vc) == nil && time.Since(vc.CheckedAt) < driftTTL {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := c.ConsoleVersion(ctx)
	if err != nil {
		return
	}
	writeVersionCache(path, v)
	if msg := driftMessage(v); msg != "" {
		fmt.Fprintln(io.Err, "unifi: warning:", msg)
	}
}

func writeVersionCache(path, version string) {
	b, _ := json.Marshal(versionCache{Version: version, CheckedAt: time.Now().UTC()})
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = os.WriteFile(path, b, 0o600)
	}
}

// driftMessage describes a newer console, or returns "".
func driftMessage(console string) string {
	if unifi.CompareVersions(console, spec.SpecVersion) <= 0 {
		return ""
	}
	return fmt.Sprintf("console runs UniFi Network %s, newer than this CLI's spec (%s); new endpoints or fields may be missing. "+
		"Update unifi, or reach them with `unifi api` (see `unifi doctor`).", console, spec.SpecVersion)
}
