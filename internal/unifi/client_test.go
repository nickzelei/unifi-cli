package unifi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tlsServer(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var gotKey string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		_, _ = w.Write([]byte(`{"applicationVersion":"10.6.109"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotKey
}

func TestPinnedFingerprint(t *testing.T) {
	srv, gotKey := tlsServer(t)
	fp := Fingerprint(srv.Certificate())
	ctx := context.Background()

	c, err := NewClient(Profile{BaseURL: srv.URL, APIKey: "k", TLSFingerprint: "SHA256:" + strings.ToUpper(fp)}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(ctx, "GET", "/v1/info", nil, nil); err != nil {
		t.Fatalf("pinned request failed: %v", err)
	}
	if *gotKey != "k" {
		t.Fatalf("X-API-Key = %q", *gotKey)
	}

	wrong := strings.Repeat("ab", 32)
	c, _ = NewClient(Profile{BaseURL: srv.URL, APIKey: "k", TLSFingerprint: wrong}, 5*time.Second)
	if _, err := c.Do(ctx, "GET", "/v1/info", nil, nil); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("want fingerprint mismatch, got %v", err)
	}

	c, _ = NewClient(Profile{BaseURL: srv.URL, APIKey: "k"}, 5*time.Second)
	if _, err := c.Do(ctx, "GET", "/v1/info", nil, nil); err == nil || !strings.Contains(err.Error(), "unifi config init") {
		t.Fatalf("want self-signed hint, got %v", err)
	}
}

func TestProbeCertificate(t *testing.T) {
	srv, _ := tlsServer(t)
	cert, trusted, err := ProbeCertificate(context.Background(), Profile{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Fatal("test certificate should not be system-trusted")
	}
	if Fingerprint(cert) != Fingerprint(srv.Certificate()) {
		t.Fatal("probed a different certificate")
	}
}

func TestAPIKeyCmd(t *testing.T) {
	srv, gotKey := tlsServer(t)
	p := Profile{BaseURL: srv.URL, APIKeyCmd: "printf ' from-cmd \\n'", Insecure: true}
	c, _ := NewClient(p, 5*time.Second)
	if _, err := c.Do(context.Background(), "GET", "/v1/info", nil, nil); err != nil {
		t.Fatal(err)
	}
	if *gotKey != "from-cmd" {
		t.Fatalf("X-API-Key = %q", *gotKey)
	}

	p.APIKeyCmd = "exit 3"
	c, _ = NewClient(p, 5*time.Second)
	if _, err := c.Do(context.Background(), "GET", "/v1/info", nil, nil); err == nil || !strings.Contains(err.Error(), "api_key_cmd failed") {
		t.Fatalf("want api_key_cmd error, got %v", err)
	}
}

func TestReadOnlyClient(t *testing.T) {
	srv, _ := tlsServer(t)
	c, _ := NewClient(Profile{BaseURL: srv.URL, APIKey: "k", Insecure: true, ReadOnly: true}, 5*time.Second)
	if _, err := c.Do(context.Background(), "GET", "/v1/info", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "get"} {
		if _, err := c.Do(context.Background(), m, "/v1/info", nil, nil); !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s: want ErrReadOnly, got %v", m, err)
		}
	}
}

func TestReadOnlyEnvFailsClosed(t *testing.T) {
	t.Setenv("UNIFI_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	for v, want := range map[string]bool{"": false, "0": false, "false": false, "No": false, "off": false, "1": true, "on": true, "y": true, "enabled": true} {
		t.Setenv("UNIFI_READ_ONLY", v)
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Profile.ReadOnly != want {
			t.Errorf("UNIFI_READ_ONLY=%q: read-only %v, want %v", v, cfg.Profile.ReadOnly, want)
		}
	}
}

func TestLoadProfilesAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("UNIFI_CONFIG", path)
	for _, k := range []string{"UNIFI_PROFILE", "UNIFI_HOST", "UNIFI_BASE_URL", "UNIFI_API_KEY", "UNIFI_SITE", "UNIFI_TLS_FINGERPRINT", "UNIFI_INSECURE", "UNIFI_READ_ONLY"} {
		t.Setenv(k, "")
	}
	err := WriteFile(path, &File{DefaultProfile: "home", Profiles: map[string]Profile{
		"home": {Host: "192.168.1.1", APIKeyCmd: "op read x", Site: "default"},
		"lab":  {Host: "10.0.0.1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", st.Mode().Perm())
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "home" || cfg.Profile.Host != "192.168.1.1" {
		t.Fatalf("got %+v", cfg)
	}
	if base, _ := cfg.Profile.Base(); base != "https://192.168.1.1/proxy/network/integration" {
		t.Fatalf("base = %q", base)
	}

	t.Setenv("UNIFI_PROFILE", "lab")
	t.Setenv("UNIFI_API_KEY", "envkey")
	cfg, _ = Load("")
	if cfg.Name != "lab" || cfg.Profile.APIKey != "envkey" {
		t.Fatalf("env overrides not applied: %+v", cfg)
	}

	if _, err := Load("nope"); err == nil {
		t.Fatal("want error for unknown explicit profile")
	}
}

func TestAPIErrorMessage(t *testing.T) {
	e := &APIError{Method: "GET", URL: "https://x/v1/sites", Status: 400,
		Body: []byte(`{"statusCode":400,"code":"api.request.invalid-filter","message":"bad filter","timestamp":"t","requestId":"r"}`)}
	if got, want := e.Error(), "HTTP 400 api.request.invalid-filter: bad filter (GET https://x/v1/sites)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	e.Body = []byte("plain text")
	if got := e.Error(); !strings.Contains(got, "HTTP 400 plain text") {
		t.Errorf("non-JSON body: %q", got)
	}
	e.Body = nil
	if got := e.Error(); !strings.Contains(got, "Bad Request") {
		t.Errorf("empty body: %q", got)
	}
}

func TestMatchSite(t *testing.T) {
	sites := []Site{
		{ID: "11111111-1111-1111-1111-111111111111", InternalReference: "default", Name: "Default"},
		{ID: "22222222-2222-2222-2222-222222222222", InternalReference: "abc123", Name: "Lab"},
	}
	for sel, want := range map[string]string{
		"default":   sites[0].ID,
		"lab":       sites[1].ID,
		sites[1].ID: sites[1].ID,
	} {
		got, err := MatchSite(sites, sel)
		if err != nil || got != want {
			t.Errorf("MatchSite(%q) = %q, %v; want %q", sel, got, err, want)
		}
	}
	if _, err := MatchSite(sites, ""); err == nil {
		t.Error("empty selector with two sites should fail")
	}
	if got, _ := MatchSite(sites[:1], ""); got != sites[0].ID {
		t.Error("empty selector with one site should pick it")
	}
	if _, err := MatchSite(sites, "missing"); err == nil {
		t.Error("unknown site should fail")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"10.6.109", "10.6.109", 0},
		{"10.6.110", "10.6.109", 1},
		{"10.10.0", "10.9.200", 1},
		{"9.5.21", "10.0.0", -1},
		{"10.6", "10.6.0", 0},
		{"10.6.109-beta", "10.6.109", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
