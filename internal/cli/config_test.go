package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickzelei/unifi-cli/internal/unifi"
)

func TestConfigInitInteractive(t *testing.T) {
	srv := httptest.NewTLSServer(http.StripPrefix("/proxy/network/integration", &mock{}))
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("UNIFI_CONFIG", path)
	t.Setenv("UNIFI_API_KEY", "")

	answers := strings.Join([]string{
		"home",          // profile
		srv.URL,         // console address
		"3",             // not a choice: asked again
		"1",             // api key command
		"echo test-key", // the command
		"",              // only site
	}, "\n") + "\n"
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"config", "init"}, IO{In: strings.NewReader(answers), Out: &out, Err: &errb, InTTY: true})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.Count(errb.String(), "Choice") != 2 {
		t.Errorf("invalid choice not re-asked:\n%s", errb.String())
	}
	f, err := unifi.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p := f.Profiles["home"]
	if p.Host != srv.URL || p.APIKeyCmd != "echo test-key" || p.Site != siteID || p.TLSFingerprint != unifi.Fingerprint(srv.Certificate()) {
		t.Errorf("profile %+v", p)
	}
	if f.DefaultProfile != "home" {
		t.Errorf("default profile %q", f.DefaultProfile)
	}

	// Re-running keeps current values on blank answers; switch to a pasted key.
	answers = "home\n\n2\ntest-key\n\n"
	errb.Reset()
	code = Run(context.Background(), []string{"config", "init"}, IO{In: strings.NewReader(answers), Out: &out, Err: &errb, InTTY: true})
	if code != 0 {
		t.Fatalf("rerun: exit %d: %s", code, errb.String())
	}
	f, _ = unifi.ReadFile(path)
	if p := f.Profiles["home"]; p.Host != srv.URL || p.APIKey != "test-key" || p.APIKeyCmd != "" || p.Site != siteID {
		t.Errorf("rerun profile %+v", p)
	}

	// Input running out mid-way is an error, not a half-filled profile.
	code = Run(context.Background(), []string{"config", "init"}, IO{In: strings.NewReader("other\n"), Out: &out, Err: &errb, InTTY: true})
	if code != exitUsage || !strings.Contains(errb.String(), "input ended") {
		t.Errorf("EOF: exit %d: %s", code, errb.String())
	}
}

func TestConfigInitNoTTYNeedsHost(t *testing.T) {
	t.Setenv("UNIFI_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	code, _, errb := run(t, "", "config", "init")
	if code != exitUsage || !strings.Contains(errb, "--host is required") {
		t.Errorf("exit %d: %s", code, errb)
	}
}

func TestConfigInitReadOnlySticks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("UNIFI_CONFIG", path)
	initWith := func(args ...string) unifi.Profile {
		t.Helper()
		args = append([]string{"config", "init", "--host", "192.168.1.1", "--api-key-cmd", "true", "--skip-check"}, args...)
		if code, _, errb := run(t, "", args...); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errb)
		}
		f, err := unifi.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return f.Profiles["default"]
	}
	if p := initWith("--read-only"); !p.ReadOnly {
		t.Fatal("--read-only not saved")
	}
	if p := initWith(); !p.ReadOnly {
		t.Fatal("re-init without --read-only turned it off")
	}
	if p := initWith("--read-only=false"); p.ReadOnly {
		t.Fatal("--read-only=false did not turn it off")
	}
}

func TestConfigReadOnlyCmd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("UNIFI_CONFIG", path)
	t.Setenv("UNIFI_READ_ONLY", "")
	t.Setenv("UNIFI_PROFILE", "")
	if err := unifi.WriteFile(path, &unifi.File{DefaultProfile: "home", Profiles: map[string]unifi.Profile{
		"home": {Host: "192.168.1.1"}, "admin": {Host: "192.168.1.1"},
	}}); err != nil {
		t.Fatal(err)
	}
	status := func(args ...string) map[string]any {
		t.Helper()
		code, out, errb := run(t, "", append([]string{"config", "read-only"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errb)
		}
		var res map[string]any
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if r := status(); r["profile"] != "home" || r["read_only"] != false {
		t.Fatalf("initial status %v", r)
	}
	if r := status("on"); r["read_only"] != true || r["profile_setting"] != true {
		t.Fatalf("on: %v", r)
	}
	if r := status("--profile", "admin"); r["read_only"] != false {
		t.Fatalf("admin should be unaffected: %v", r)
	}
	// --all covers every profile, even one whose own setting is off.
	if r := status("on", "--all"); r["all_profiles"] != true {
		t.Fatalf("on --all: %v", r)
	}
	if r := status("--profile", "admin"); r["read_only"] != true || r["profile_setting"] != false {
		t.Fatalf("admin under --all: %v", r)
	}
	status("off", "--all")
	if r := status("off"); r["read_only"] != false {
		t.Fatalf("off: %v", r)
	}
	for _, args := range [][]string{{"maybe"}, {"on", "--all", "--profile", "home"}, {"on", "--profile", "nope"}} {
		if code, _, _ := run(t, "", append([]string{"config", "read-only"}, args...)...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}
