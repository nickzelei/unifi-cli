package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"time"

	"github.com/nickzelei/unifi-cli/internal/spec"
	"github.com/nickzelei/unifi-cli/internal/unifi"
)

type checkResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail, skip
	Detail string `json:"detail"`
}

// certWarnDays is how close to expiry a pinned certificate gets a warning.
const certWarnDays = 30

func doctorCmd(ctx context.Context, args []string, io IO) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	fs.Usage = func() {
		fmt.Fprint(io.Out, `Check the configuration and the connection to the console.

Usage:
  unifi doctor [--profile <name>] [--site <s>] [--timeout <dur>]

Runs config, tls, api_key, api, site and spec checks in order; a failed
check skips the ones that depend on it. Prints JSON. Exits 1 if any check
fails (warnings don't fail).
`)
	}
	profile := fs.String("profile", "", "config profile")
	site := fs.String("site", "", "site to resolve (default: configured site)")
	timeout := fs.Duration("timeout", 15*time.Second, "timeout for each network check")
	if err := fs.Parse(args); err != nil {
		return flagErr(err, "doctor")
	}

	var checks []checkResult
	add := func(name, status, format string, a ...any) {
		checks = append(checks, checkResult{name, status, fmt.Sprintf(format, a...)})
	}
	skipRest := func(names ...string) {
		for _, n := range names {
			add(n, "skip", "skipped: an earlier check failed")
		}
	}

	failed := func() error {
		b, err := json.Marshal(map[string]any{"ok": false, "spec_version": spec.SpecVersion, "checks": checks})
		if err != nil {
			return err
		}
		if err := emit(io, b, true, nil); err != nil {
			return err
		}
		return errors.New("doctor: some checks failed")
	}

	// config
	cfg, err := unifi.Load(*profile)
	if err != nil {
		add("config", "fail", "%v", err)
		skipRest("tls", "api_key", "api", "site", "spec")
		return failed()
	}
	p := cfg.Profile
	base, err := p.Base()
	if err != nil {
		add("config", "fail", "profile %q in %s: %v", cfg.Name, cfg.Path, err)
		skipRest("tls", "api_key", "api", "site", "spec")
		return failed()
	}
	add("config", "ok", "profile %q from %s; API at %s", cfg.Name, cfg.Path, base)

	// tls
	tlsOK := checkTLS(ctx, p, base, *timeout, add)

	// api_key
	keyCtx, cancel := context.WithTimeout(ctx, *timeout)
	key, err := p.ResolveAPIKey(keyCtx)
	cancel()
	switch {
	case err != nil:
		add("api_key", "fail", "%v", err)
	case p.APIKeyCmd != "" && p.APIKey == "":
		add("api_key", "ok", "read from api_key_cmd")
	default:
		add("api_key", "ok", "set inline or via UNIFI_API_KEY")
	}
	if err != nil || !tlsOK {
		skipRest("api", "site", "spec")
		return failed()
	}
	p.APIKey = key // don't run api_key_cmd again

	// api
	c, err := unifi.NewClient(p, *timeout)
	if err != nil {
		add("api", "fail", "%v", err)
		skipRest("site", "spec")
		return failed()
	}
	version, err := c.ConsoleVersion(ctx)
	if err != nil {
		add("api", "fail", "%v", err)
		skipRest("site", "spec")
		return failed()
	}
	add("api", "ok", "UniFi Network %s", version)
	if path, err := versionCachePath(c.Base()); err == nil {
		writeVersionCache(path, version)
	}

	// site
	sel := *site
	if sel == "" {
		sel = p.Site
	}
	if sites, err := c.ListSites(ctx); err != nil {
		add("site", "fail", "list sites: %v", err)
	} else if id, err := unifi.MatchSite(sites, sel); err != nil {
		add("site", "fail", "%v", err)
	} else {
		add("site", "ok", "%s (%d site(s) on the console)", id, len(sites))
	}

	// spec
	switch cmp := unifi.CompareVersions(version, spec.SpecVersion); {
	case cmp > 0:
		add("spec", "warn", "console %s is newer than the embedded spec %s: new endpoints or fields may be missing. "+
			"Update unifi, use `unifi api` for new endpoints, or in a checkout run `mise run spec:update`.", version, spec.SpecVersion)
	case cmp < 0:
		add("spec", "warn", "console %s is older than the embedded spec %s: some commands or fields may not exist on this console (expect HTTP 404 or 400).", version, spec.SpecVersion)
	default:
		add("spec", "ok", "console matches the embedded spec %s", spec.SpecVersion)
	}

	ok := true
	for _, ch := range checks {
		if ch.Status == "fail" {
			ok = false
		}
	}
	if !ok {
		return failed()
	}
	b, err := json.Marshal(map[string]any{"ok": true, "spec_version": spec.SpecVersion, "checks": checks})
	if err != nil {
		return err
	}
	return emit(io, b, true, nil)
}

// checkTLS records the tls check and reports whether requests can proceed.
func checkTLS(ctx context.Context, p unifi.Profile, base string, timeout time.Duration, add func(name, status, format string, a ...any)) bool {
	u, err := url.Parse(base)
	if err != nil {
		add("tls", "fail", "bad base URL %q: %v", base, err)
		return false
	}
	if u.Scheme != "https" {
		add("tls", "warn", "plain %s: traffic, including the API key, is not encrypted", u.Scheme)
		return true
	}
	if p.Insecure {
		add("tls", "warn", "certificate verification is disabled (insecure); re-run `unifi config init` to pin the certificate")
		return true
	}
	probe := p
	probe.Host = u.Scheme + "://" + u.Host
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cert, trusted, err := unifi.ProbeCertificate(ctx, probe)
	if err != nil {
		add("tls", "fail", "connect to %s: %v", u.Host, err)
		return false
	}
	days := int(time.Until(cert.NotAfter).Hours() / 24)
	expiry := fmt.Sprintf("expires %s", cert.NotAfter.Format("2006-01-02"))
	switch {
	case p.TLSFingerprint != "":
		want, err := unifi.NormalizeFingerprint(p.TLSFingerprint)
		if err != nil {
			add("tls", "fail", "configured tls_fingerprint: %v", err)
			return false
		}
		if got := unifi.Fingerprint(cert); got != want {
			add("tls", "fail", "certificate changed: console presents sha256:%s but sha256:%s is pinned. "+
				"If the console's certificate was renewed on purpose, re-run `unifi config init` to re-pin it", got, want)
			return false
		}
	case !trusted:
		add("tls", "fail", "certificate is not trusted by the system and none is pinned; run `unifi config init` to pin it")
		return false
	}
	how := "pinned certificate matches"
	if p.TLSFingerprint == "" {
		how = "certificate is trusted by the system"
	}
	switch {
	case days < 0 && p.TLSFingerprint != "":
		// Pinning checks the fingerprint, not dates, so requests still work.
		add("tls", "warn", "%s but it expired on %s; requests still work while it's pinned", how, cert.NotAfter.Format("2006-01-02"))
	case days < 0:
		add("tls", "fail", "%s but it expired on %s", how, cert.NotAfter.Format("2006-01-02"))
		return false
	case days < certWarnDays:
		add("tls", "warn", "%s; %s (in %d days)", how, expiry, days)
	default:
		add("tls", "ok", "%s; %s", how, expiry)
	}
	return true
}
