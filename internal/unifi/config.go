package unifi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Profile holds the settings for one UniFi console.
type Profile struct {
	// Host is the console address, e.g. "192.168.1.1" or "https://unifi.lan".
	Host string `json:"host,omitempty"`
	// BaseURL overrides the integration API base URL derived from Host.
	BaseURL string `json:"base_url,omitempty"`
	// APIKey is the integration API key. Prefer APIKeyCmd to keep it off disk.
	APIKey string `json:"api_key,omitempty"`
	// APIKeyCmd is a shell command whose stdout is the API key, e.g. "op read op://vault/unifi/credential".
	APIKeyCmd string `json:"api_key_cmd,omitempty"`
	// Site is the default site: a UUID, internal reference (e.g. "default"), or name.
	Site string `json:"site,omitempty"`
	// TLSFingerprint pins the console's leaf certificate by its SHA-256 hex digest.
	TLSFingerprint string `json:"tls_fingerprint,omitempty"`
	// Insecure disables TLS verification entirely.
	Insecure bool `json:"insecure,omitempty"`
	// ReadOnly makes the client refuse every request except GET.
	ReadOnly bool `json:"read_only,omitempty"`
}

// File is the on-disk config file.
type File struct {
	DefaultProfile string             `json:"default_profile,omitempty"`
	Profiles       map[string]Profile `json:"profiles,omitempty"`
}

// Config is a resolved profile ready for use.
type Config struct {
	Name    string
	Path    string
	Profile Profile
}

// ConfigPath returns the config file location: $UNIFI_CONFIG, else
// $XDG_CONFIG_HOME/unifi/config.json, else ~/.config/unifi/config.json.
func ConfigPath() (string, error) {
	if p := os.Getenv("UNIFI_CONFIG"); p != "" {
		return p, nil
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "unifi", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "unifi", "config.json"), nil
}

// ReadFile loads the config file. A missing file yields an empty File.
func ReadFile(path string) (*File, error) {
	f := &File{Profiles: map[string]Profile{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]Profile{}
	}
	return f, nil
}

// WriteFile saves the config file with owner-only permissions.
func WriteFile(path string, f *File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// ProfileName picks the active profile: explicit name, $UNIFI_PROFILE,
// the file's default, or "default".
func ProfileName(explicit string, f *File) string {
	switch {
	case explicit != "":
		return explicit
	case os.Getenv("UNIFI_PROFILE") != "":
		return os.Getenv("UNIFI_PROFILE")
	case f != nil && f.DefaultProfile != "":
		return f.DefaultProfile
	}
	return "default"
}

// Load resolves the active profile from the config file and UNIFI_* env overrides.
func Load(profile string) (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	f, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	name := ProfileName(profile, f)
	p, ok := f.Profiles[name]
	if !ok && profile != "" {
		return nil, fmt.Errorf("profile %q not found in %s (have: %s)", name, path, strings.Join(profileNames(f), ", "))
	}
	applyEnv(&p)
	return &Config{Name: name, Path: path, Profile: p}, nil
}

func profileNames(f *File) []string {
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func applyEnv(p *Profile) {
	set := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	set(&p.Host, "UNIFI_HOST")
	set(&p.BaseURL, "UNIFI_BASE_URL")
	set(&p.Site, "UNIFI_SITE")
	set(&p.TLSFingerprint, "UNIFI_TLS_FINGERPRINT")
	if v := os.Getenv("UNIFI_API_KEY"); v != "" {
		p.APIKey, p.APIKeyCmd = v, ""
	}
	if truthy(os.Getenv("UNIFI_INSECURE")) {
		p.Insecure = true
	}
	// UNIFI_READ_ONLY can only turn read-only on, never off a profile that has it.
	if truthy(os.Getenv("UNIFI_READ_ONLY")) {
		p.ReadOnly = true
	}
}

func truthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// Allows reports whether the profile may send a request with this method:
// a read-only profile allows only GET.
func (p Profile) Allows(method string) bool {
	return !p.ReadOnly || method == http.MethodGet
}

// Base returns the integration API base URL, without a trailing slash.
func (p Profile) Base() (string, error) {
	if p.BaseURL != "" {
		return strings.TrimRight(p.BaseURL, "/"), nil
	}
	o, err := p.Origin()
	if err != nil {
		return "", err
	}
	return o + "/proxy/network/integration", nil
}

// Origin returns the console origin, e.g. "https://192.168.1.1".
func (p Profile) Origin() (string, error) {
	h := strings.TrimRight(p.Host, "/")
	if h == "" {
		return "", errors.New("no host configured: run `unifi config init --host <ip>` or set UNIFI_HOST")
	}
	if !strings.Contains(h, "://") {
		h = "https://" + h
	}
	return h, nil
}

// ResolveAPIKey returns the API key, running APIKeyCmd if needed.
func (p Profile) ResolveAPIKey(ctx context.Context) (string, error) {
	if p.APIKey != "" {
		return p.APIKey, nil
	}
	if p.APIKeyCmd == "" {
		return "", errors.New("no API key configured: set api_key_cmd via `unifi config init` or set UNIFI_API_KEY")
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-c", p.APIKeyCmd)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("api_key_cmd failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", errors.New("api_key_cmd printed an empty key")
	}
	return key, nil
}

// Redacted returns a copy safe to print.
func (p Profile) Redacted() Profile {
	if p.APIKey != "" {
		p.APIKey = "<redacted>"
	}
	return p
}
