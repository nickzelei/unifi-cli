package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nickzelei/unifi-cli/internal/unifi"
)

func configCmd(ctx context.Context, args []string, io IO) error {
	if len(args) == 0 || hasHelpFlag(args[:1]) {
		fmt.Fprint(io.Out, `Manage connection profiles.

Usage:
  unifi config init --host <ip> [--api-key-cmd <cmd>] [--site <s>] [flags]
  unifi config init                (no flags, at a terminal: prompts for each setting)
  unifi config show [--profile <name>]
  unifi config path

Config file: $UNIFI_CONFIG, else $XDG_CONFIG_HOME/unifi/config.json, else ~/.config/unifi/config.json.
Env overrides: UNIFI_PROFILE UNIFI_HOST UNIFI_BASE_URL UNIFI_API_KEY UNIFI_SITE
               UNIFI_TLS_FINGERPRINT UNIFI_INSECURE UNIFI_NO_VERSION_CHECK
Check a profile with: unifi doctor [--profile <name>]
`)
		return nil
	}
	switch args[0] {
	case "init":
		return configInit(ctx, args[1:], io)
	case "show":
		return configShow(args[1:], io)
	case "path":
		p, err := unifi.ConfigPath()
		if err != nil {
			return err
		}
		fmt.Fprintln(io.Out, p)
		return nil
	}
	return usagef("unknown config subcommand %q (init, show, path)", args[0])
}

func configInit(ctx context.Context, args []string, io IO) error {
	fs := flag.NewFlagSet("config init", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	name := fs.String("profile", "default", "profile name")
	host := fs.String("host", "", "console address, e.g. 192.168.1.1 (required)")
	baseURL := fs.String("base-url", "", "override the integration API base URL")
	keyCmd := fs.String("api-key-cmd", "", `shell command printing the API key, e.g. "op read op://Private/UniFi/credential"`)
	key := fs.String("api-key", "", "API key stored in plain text (prefer --api-key-cmd or $UNIFI_API_KEY)")
	site := fs.String("site", "", "default site (id, internal reference, or name); default: the only site")
	fingerprint := fs.String("tls-fingerprint", "", "pin this SHA-256 certificate fingerprint instead of the one presented now")
	insecure := fs.Bool("insecure", false, "disable TLS verification (not recommended)")
	skipCheck := fs.Bool("skip-check", false, "save without contacting the console")
	makeDefault := fs.Bool("default", false, "make this the default profile")
	if err := fs.Parse(args); err != nil {
		return flagErr(err, "config init")
	}
	if fs.NArg() > 0 {
		return usagef("unexpected argument %q", fs.Arg(0))
	}

	path, err := unifi.ConfigPath()
	if err != nil {
		return err
	}
	f, err := unifi.ReadFile(path)
	if err != nil {
		return err
	}
	// No flags at a terminal: ask for them instead.
	interactive := fs.NFlag() == 0 && io.InTTY
	if interactive {
		if err := promptInit(ctx, io, f, initAnswers{name, host, keyCmd, key, site}); err != nil {
			return err
		}
	}
	p := f.Profiles[*name]
	if *host != "" {
		p.Host = *host
	}
	if *baseURL != "" {
		p.BaseURL = *baseURL
	}
	if *keyCmd != "" {
		p.APIKeyCmd, p.APIKey = *keyCmd, ""
	}
	if *key != "" {
		p.APIKey, p.APIKeyCmd = *key, ""
	}
	if *site != "" {
		p.Site = *site
	}
	p.Insecure = *insecure
	if p.Host == "" && p.BaseURL == "" {
		return usagef("--host is required (or run `unifi config init` with no flags at a terminal to be prompted)")
	}

	result := map[string]any{"profile": *name, "path": path}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch {
	case *insecure:
		p.TLSFingerprint = ""
	case *fingerprint != "":
		fp, err := unifi.NormalizeFingerprint(*fingerprint)
		if err != nil {
			return usageError{err.Error()}
		}
		p.TLSFingerprint = fp
	case !*skipCheck:
		cert, trusted, err := unifi.ProbeCertificate(ctx, p)
		if err != nil {
			return fmt.Errorf("connect to console: %w", err)
		}
		if trusted {
			p.TLSFingerprint = ""
			result["tls"] = "system-trusted certificate"
		} else {
			p.TLSFingerprint = unifi.Fingerprint(cert)
			result["tls"] = fmt.Sprintf("pinned self-signed certificate (CN=%s, expires %s)", cert.Subject.CommonName, cert.NotAfter.Format("2006-01-02"))
			fmt.Fprintf(io.Err, "unifi: pinning certificate sha256:%s\n", p.TLSFingerprint)
		}
	}

	if !*skipCheck {
		check := p
		if check.APIKey == "" && check.APIKeyCmd == "" {
			check.APIKey = os.Getenv("UNIFI_API_KEY")
		}
		c, err := unifi.NewClient(check, 30*time.Second)
		if err != nil {
			return err
		}
		b, err := c.Do(ctx, "GET", "/v1/info", nil, nil)
		if err != nil {
			return fmt.Errorf("check failed (use --skip-check to save anyway): %w", err)
		}
		var info map[string]any
		_ = json.Unmarshal(b, &info)
		result["application"] = info
		sites, err := c.ListSites(ctx)
		if err != nil {
			return fmt.Errorf("list sites: %w", err)
		}
		id, err := unifi.MatchSite(sites, p.Site)
		if err != nil {
			return err
		}
		p.Site = id
	}

	f.Profiles[*name] = p
	if f.DefaultProfile == "" || *makeDefault {
		f.DefaultProfile = *name
	}
	if err := unifi.WriteFile(path, f); err != nil {
		return err
	}
	result["settings"] = p.Redacted()
	result["default_profile"] = f.DefaultProfile
	b, _ := json.Marshal(result)
	if err := emit(io, b, true, nil); err != nil {
		return err
	}
	if interactive {
		fmt.Fprintln(io.Err, "unifi: saved. Check it any time with `unifi doctor`.")
	}
	return nil
}

func configShow(args []string, io IO) error {
	fs := flag.NewFlagSet("config show", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	profile := fs.String("profile", "", "profile name")
	if err := fs.Parse(args); err != nil {
		return flagErr(err, "config show")
	}
	cfg, err := unifi.Load(*profile)
	if err != nil {
		return err
	}
	f, err := unifi.ReadFile(cfg.Path)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	base, _ := cfg.Profile.Base()
	b, err := json.Marshal(map[string]any{
		"path":     cfg.Path,
		"profile":  cfg.Name,
		"profiles": names,
		"base_url": base,
		"settings": cfg.Profile.Redacted(),
	})
	if err != nil {
		return err
	}
	return emit(io, b, true, nil)
}
