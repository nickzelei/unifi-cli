package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nickzelei/unifi-cli/internal/spec"
	"github.com/nickzelei/unifi-cli/internal/unifi"
)

const (
	siteID   = "0b1e2a3c-0000-4000-8000-000000000001"
	deviceID = "4f5b2c3a-1111-2222-3333-444455556666"
)

// mock is a fake console that records requests.
type mock struct {
	mu      sync.Mutex
	reqs    []string
	version string // applicationVersion served by /v1/info
}

func (m *mock) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.reqs...)
}

func (m *mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.reqs = append(m.reqs, r.Method+" "+r.URL.RequestURI())
	m.mu.Unlock()
	if r.Header.Get("X-API-Key") != "test-key" {
		http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	p := r.URL.Path
	switch {
	case p == "/v1/info":
		v := m.version
		if v == "" {
			v = spec.SpecVersion
		}
		fmt.Fprintf(w, `{"applicationVersion":%q}`, v)
	case p == "/v1/sites":
		fmt.Fprintf(w, `{"offset":0,"limit":200,"count":1,"totalCount":1,"data":[{"id":%q,"internalReference":"default","name":"Default"}]}`, siteID)
	case p == "/v1/sites/"+siteID+"/devices":
		// Five devices, served in pages of at most `limit`.
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit == 0 {
			limit = 2
		}
		var items []string
		for i := offset; i < 5 && i < offset+limit; i++ {
			name := fmt.Sprintf("Device %d", i)
			if i >= 3 {
				name = "Twin"
			}
			items = append(items, fmt.Sprintf(`{"id":%q,"name":%q,"macAddress":"aa:bb:cc:00:00:0%d","ipAddress":"10.0.0.%d","state":"ONLINE","interfaces":{"ports":[{"idx":1}]}}`, devID(i), name, i, i))
		}
		fmt.Fprintf(w, `{"offset":%d,"limit":%d,"count":%d,"totalCount":5,"data":[%s]}`, offset, limit, len(items), strings.Join(items, ","))
	case strings.HasSuffix(p, "/actions"):
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(p, "/v1/sites/"+siteID+"/devices/"):
		fmt.Fprintf(w, `{"id":%q}`, strings.TrimPrefix(p, "/v1/sites/"+siteID+"/devices/"))
	case p == "/v1/sites/"+siteID+"/wans":
		_, _ = w.Write([]byte(`{"data":[{"name":"WAN1"}],"totalCount":1}`))
	default:
		http.Error(w, `{"statusCode":404,"message":"not found"}`, http.StatusNotFound)
	}
}

func devID(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", i) }

func setup(t *testing.T) *mock {
	t.Helper()
	m := &mock{}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	t.Setenv("UNIFI_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("UNIFI_BASE_URL", srv.URL)
	t.Setenv("UNIFI_API_KEY", "test-key")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("UNIFI_NO_VERSION_CHECK", "1")
	for _, k := range []string{"UNIFI_PROFILE", "UNIFI_HOST", "UNIFI_SITE", "UNIFI_TLS_FINGERPRINT", "UNIFI_INSECURE", "UNIFI_READ_ONLY"} {
		t.Setenv(k, "")
	}
	return m
}

func run(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Run(context.Background(), args, IO{In: strings.NewReader(stdin), Out: &out, Err: &errb})
	return code, out.String(), errb.String()
}

func TestListResolvesOnlySite(t *testing.T) {
	m := setup(t)
	code, out, errOut := run(t, "", "devices", "list")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, `"totalCount":5`) {
		t.Fatalf("unexpected output: %s", out)
	}
	want := []string{"GET /v1/sites?limit=200", "GET /v1/sites/" + siteID + "/devices"}
	if got := m.seen(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
}

func TestSiteByUUIDSkipsLookup(t *testing.T) {
	m := setup(t)
	if code, _, errOut := run(t, "", "devices", "get", deviceID, "--site", siteID); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := m.seen(); len(got) != 1 || !strings.HasSuffix(got[0], "/devices/"+deviceID) {
		t.Fatalf("requests = %v", got)
	}
}

func TestAllPagesAndFields(t *testing.T) {
	m := setup(t)
	code, out, errOut := run(t, "", "devices", "list", "--all", "--limit", "2", "--site", "default", "--fields", "id,interfaces.ports.idx")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var page struct {
		Count, TotalCount int
		Data              []map[string]any
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatalf("bad JSON %q: %v", out, err)
	}
	if page.Count != 5 || page.TotalCount != 5 || len(page.Data) != 5 {
		t.Fatalf("got %+v", page)
	}
	if _, ok := page.Data[0]["name"]; ok {
		t.Fatalf("--fields did not drop name: %v", page.Data[0])
	}
	if fmt.Sprint(page.Data[4]) != "map[id:"+devID(4)+" interfaces:map[ports:[map[idx:1]]]]" {
		t.Fatalf("projection = %v", page.Data[4])
	}
	// sites lookup + 3 pages (2+2+1)
	if n := len(m.seen()); n != 4 {
		t.Fatalf("made %d requests: %v", n, m.seen())
	}
}

func TestIDByNameMACOrIP(t *testing.T) {
	list := "GET /v1/sites/" + siteID + "/devices?limit=200&offset=0"
	for sel, want := range map[string]int{
		"device 1":          1, // name, any case
		"AA-BB-CC-00-00-02": 2, // MAC, any case or separator
		"10.0.0.0":          0, // IP
	} {
		m := setup(t)
		code, out, errOut := run(t, "", "devices", "get", sel, "--site", siteID)
		if code != 0 || !strings.Contains(out, devID(want)) {
			t.Errorf("%q: exit %d, out %q, err %q", sel, code, out, errOut)
			continue
		}
		if got := m.seen(); fmt.Sprint(got) != fmt.Sprint([]string{list, "GET /v1/sites/" + siteID + "/devices/" + devID(want)}) {
			t.Errorf("%q: requests = %v", sel, got)
		}
	}
}

func TestIDLookupErrors(t *testing.T) {
	setup(t)
	code, _, errOut := run(t, "", "devices", "get", "twin", "--site", siteID)
	if code != exitErr || !strings.Contains(errOut, "matches 2 devices") || !strings.Contains(errOut, devID(3)) || !strings.Contains(errOut, devID(4)) {
		t.Errorf("ambiguous: exit %d, stderr %q", code, errOut)
	}
	code, _, errOut = run(t, "", "devices", "get", "nope", "--site", siteID)
	if code != exitErr || !strings.Contains(errOut, `no device matches "nope" by name/macAddress/ipAddress`) {
		t.Errorf("missing: exit %d, stderr %q", code, errOut)
	}
}

func TestIDLookupWithLaterArgs(t *testing.T) {
	setup(t)
	code, out, errOut := run(t, "", "devices", "port-action", "Device 2", "7", "--site", siteID, "--data", `{"action":"POWER_CYCLE"}`, "--dry-run")
	if code != 0 || !strings.Contains(out, "/devices/"+devID(2)+"/interfaces/ports/7/actions") {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
}

func TestDestructiveNeedsYes(t *testing.T) {
	m := setup(t)
	body := `{"action":"RESTART"}`
	code, _, errOut := run(t, "", "devices", "action", deviceID, "--data", body, "--site", siteID)
	if code != exitUsage || !strings.Contains(errOut, "--yes") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if len(m.seen()) != 0 {
		t.Fatalf("sent requests without --yes: %v", m.seen())
	}
	if code, _, errOut := run(t, "", "devices", "action", deviceID, "--data", body, "--site", siteID, "--yes"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestNoValidateAndWarnings(t *testing.T) {
	m := setup(t)
	args := []string{"devices", "action", deviceID, "--site", siteID, "--yes"}
	code, _, errOut := run(t, "", append(args, "--data", `{"action":"REBOOT"}`, "--no-validate")...)
	if code != 0 || len(m.seen()) != 1 {
		t.Fatalf("--no-validate: exit %d, stderr %q, requests %v", code, errOut, m.seen())
	}
	code, _, errOut = run(t, "", append(args, "--data", `{"action":"RESTART","later":true}`)...)
	if code != 0 || !strings.Contains(errOut, "warning: --data $.later: not in the spec") {
		t.Fatalf("unknown field: exit %d, stderr %q", code, errOut)
	}
}

func TestDryRun(t *testing.T) {
	m := setup(t)
	code, out, errOut := run(t, `{"action":"RESTART"}`, "devices", "action", deviceID, "--data", "-", "--site", siteID, "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var req struct {
		Method, URL string
		Body        map[string]any
	}
	if err := json.Unmarshal([]byte(out), &req); err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL, "/v1/sites/"+siteID+"/devices/"+deviceID+"/actions") || req.Body["action"] != "RESTART" {
		t.Fatalf("dry run = %+v", req)
	}
	if len(m.seen()) != 0 {
		t.Fatalf("dry run sent requests: %v", m.seen())
	}
}

func TestUsageErrors(t *testing.T) {
	setup(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"devices", "get"}, "want 1"},
		{[]string{"firewall", "policies", "ordering", "get", "--site", siteID, "--source-firewall-zone-id", "x", "--destination-firewall-zone-id", "y"}, "must be a UUID"},
		{[]string{"dns", "policies", "create", "--site", siteID}, "--data is required"},
		{[]string{"dns", "policies", "create", "--site", siteID, "--data", "{nope"}, "not valid JSON"},
		{[]string{"devices", "list", "--bogus"}, "flag provided but not defined"},
		{[]string{"devices", "action", deviceID, "--site", siteID, "--data", `{"action":"REBOOT"}`, "--yes"}, `$.action: must be one of RESTART, got "REBOOT"`},
		{[]string{"devices", "list", "--limit", "x"}, "must be an integer"},
		{[]string{"firewall", "policies", "ordering", "get", "--site", siteID}, "--source-firewall-zone-id is required"},
		{[]string{"nope"}, "unknown command"},
		{[]string{"devices"}, "needs a subcommand"},
	}
	for _, c := range cases {
		code, _, errOut := run(t, "", c.args...)
		if code != exitUsage || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: exit %d, stderr %q; want exit 2 containing %q", c.args, code, errOut, c.want)
		}
	}
}

func TestAPIErrorExitsOne(t *testing.T) {
	setup(t)
	code, out, errOut := run(t, "", "networks", "get", deviceID, "--site", siteID)
	if code != exitErr || out != "" || !strings.Contains(errOut, "HTTP 404") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestRawAPI(t *testing.T) {
	m := setup(t)
	code, out, errOut := run(t, "", "api", "GET", "/v1/sites/{siteId}/wans", "--query", "limit=5")
	if code != 0 || !strings.Contains(out, "WAN1") {
		t.Fatalf("exit %d, out %q, err %q", code, out, errOut)
	}
	if got := m.seen(); got[len(got)-1] != "GET /v1/sites/"+siteID+"/wans?limit=5" {
		t.Fatalf("requests = %v", got)
	}
	if code, _, _ := run(t, "", "api", "DELETE", "/v1/sites/{siteId}/networks/x"); code != exitUsage {
		t.Fatalf("raw DELETE without --yes: exit %d", code)
	}
}

func TestReadOnly(t *testing.T) {
	m := setup(t)
	t.Setenv("UNIFI_READ_ONLY", "1")
	restart := []string{"devices", "action", deviceID, "--site", siteID, "--data", `{"action":"RESTART"}`}
	refused := func(args ...string) {
		t.Helper()
		sent := len(m.seen())
		code, _, errOut := run(t, "", args...)
		if code != exitUsage || !strings.Contains(errOut, "is read-only") {
			t.Errorf("%v: exit %d, stderr %q", args, code, errOut)
		}
		if got := m.seen(); len(got) != sent {
			t.Errorf("%v: sent requests %v", args, got[sent:])
		}
	}
	refused(restart...)
	refused(append(restart, "--yes")...)
	refused("api", "DELETE", "/v1/sites/{siteId}/networks/x", "--yes")
	refused("api", "post", "/v1/sites/{siteId}/networks")
	for _, args := range [][]string{
		{"devices", "list", "--site", siteID},
		{"api", "GET", "/v1/sites/{siteId}/wans"},
		append(restart, "--dry-run"),
	} {
		if code, _, errOut := run(t, "", args...); code != 0 {
			t.Errorf("%v: exit %d: %s", args, code, errOut)
		}
	}

	// The env var can't switch off a read-only profile, or a read-only file.
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("UNIFI_CONFIG", path)
	t.Setenv("UNIFI_READ_ONLY", "0")
	for _, f := range []*unifi.File{
		{Profiles: map[string]unifi.Profile{"default": {ReadOnly: true}}},
		{ReadOnly: true, Profiles: map[string]unifi.Profile{"default": {}, "admin": {}}},
	} {
		if err := unifi.WriteFile(path, f); err != nil {
			t.Fatal(err)
		}
		refused(append(restart, "--yes")...)
	}
	refused(append(restart, "--yes", "--profile", "admin")...)
}

func TestHelpRendersForEveryOp(t *testing.T) {
	for i := range spec.Ops {
		op := &spec.Ops[i]
		args := append(append([]string{}, op.Command...), "--help")
		code, out, errOut := run(t, "", args...)
		if code != 0 || !strings.Contains(out, op.Path) {
			t.Errorf("%s --help: exit %d, stderr %q", op.Name(), code, errOut)
		}
	}
}

func TestHelpFilter(t *testing.T) {
	code, out, _ := run(t, "", "help", "filter")
	if code != 0 || !strings.Contains(out, "containsExactly") {
		t.Fatalf("exit %d, out %q", code, out)
	}
}

func TestCommandsJSON(t *testing.T) {
	code, out, _ := run(t, "", "commands", "--json")
	var cmds []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &cmds) != nil || len(cmds) != len(spec.Ops) {
		t.Fatalf("exit %d, %d commands", code, len(cmds))
	}
}

func TestSchemaVariants(t *testing.T) {
	code, out, errOut := run(t, "", "schema", "create-or-update-dns-policy", "--depth", "2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var s struct {
		Variants map[string]struct {
			Properties map[string]map[string]any
			Required   []string
		} `json:"x-variants"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	a, ok := s.Variants["A_RECORD"]
	if !ok || a.Properties["type"]["const"] != "A_RECORD" || a.Properties["ipv4Address"] == nil {
		t.Fatalf("A_RECORD variant = %+v", a)
	}
}

// Every UUID path arg should be resolvable by name, so a spec update that
// breaks the "list lives at the parent path" convention is caught.
func TestEveryUUIDArgHasLookup(t *testing.T) {
	for i := range spec.Ops {
		op := &spec.Ops[i]
		for _, a := range op.Args {
			if a.Format == "uuid" && lookupOp(op, a) == nil {
				t.Errorf("%s <%s>: no list op at the parent path", op.Name(), a.Flag)
			}
		}
	}
}

type doctorOut struct {
	OK     bool
	Checks []checkResult
}

func doctor(t *testing.T) (int, map[string]checkResult) {
	t.Helper()
	code, out, errOut := run(t, "", "doctor")
	var d doctorOut
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("doctor output %q: %v (stderr %q)", out, err, errOut)
	}
	byName := map[string]checkResult{}
	for _, c := range d.Checks {
		byName[c.Name] = c
	}
	if (code == 0) != d.OK {
		t.Fatalf("exit %d but ok=%v", code, d.OK)
	}
	return code, byName
}

func wantStatus(t *testing.T, checks map[string]checkResult, want map[string]string) {
	t.Helper()
	for name, status := range want {
		if checks[name].Status != status {
			t.Errorf("%s: status %q (%s), want %q", name, checks[name].Status, checks[name].Detail, status)
		}
	}
}

func TestDoctor(t *testing.T) {
	setup(t)
	code, checks := doctor(t)
	if code != 0 {
		t.Fatalf("exit %d: %+v", code, checks)
	}
	wantStatus(t, checks, map[string]string{"config": "ok", "tls": "warn", "api_key": "ok", "api": "ok", "site": "ok", "spec": "ok"})
	if !strings.Contains(checks["site"].Detail, siteID) {
		t.Errorf("site detail %q", checks["site"].Detail)
	}
	if !strings.HasSuffix(checks["config"].Detail, "; read-write") {
		t.Errorf("config detail %q", checks["config"].Detail)
	}
	t.Setenv("UNIFI_READ_ONLY", "1")
	if _, checks = doctor(t); !strings.HasSuffix(checks["config"].Detail, "; read-only") {
		t.Errorf("read-only config detail %q", checks["config"].Detail)
	}
	t.Setenv("UNIFI_READ_ONLY", "")

	t.Setenv("UNIFI_API_KEY", "")
	code, checks = doctor(t)
	if code != exitErr {
		t.Fatalf("no key: exit %d", code)
	}
	wantStatus(t, checks, map[string]string{"api_key": "fail", "api": "skip", "site": "skip", "spec": "skip"})
}

func TestDoctorNewerConsole(t *testing.T) {
	m := setup(t)
	m.version = "999.0.0"
	code, checks := doctor(t)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantStatus(t, checks, map[string]string{"spec": "warn"})
	if !strings.Contains(checks["spec"].Detail, "newer than the embedded spec") {
		t.Errorf("spec detail %q", checks["spec"].Detail)
	}
}

func TestDoctorPinnedTLS(t *testing.T) {
	setup(t)
	srv := httptest.NewTLSServer(&mock{})
	t.Cleanup(srv.Close)
	t.Setenv("UNIFI_BASE_URL", srv.URL)
	t.Setenv("UNIFI_TLS_FINGERPRINT", unifi.Fingerprint(srv.Certificate()))
	code, checks := doctor(t)
	if code != 0 {
		t.Fatalf("exit %d: %+v", code, checks)
	}
	wantStatus(t, checks, map[string]string{"tls": "ok", "api": "ok"})

	t.Setenv("UNIFI_TLS_FINGERPRINT", strings.Repeat("ab", 32))
	code, checks = doctor(t)
	if code != exitErr || !strings.Contains(checks["tls"].Detail, "certificate changed") {
		t.Fatalf("wrong pin: exit %d, tls %+v", code, checks["tls"])
	}
	wantStatus(t, checks, map[string]string{"api": "skip"})
}

func TestDriftWarnsOncePerTTL(t *testing.T) {
	m := setup(t)
	m.version = "999.0.0"
	t.Setenv("UNIFI_NO_VERSION_CHECK", "")
	info := func() int {
		n := 0
		for _, r := range m.seen() {
			if r == "GET /v1/info" {
				n++
			}
		}
		return n
	}
	code, out, errOut := run(t, "", "devices", "get", deviceID, "--site", siteID)
	if code != 0 || !strings.Contains(out, deviceID) || !strings.Contains(errOut, "newer than this CLI's spec") || info() != 1 {
		t.Fatalf("first run: exit %d, stderr %q, info requests %d", code, errOut, info())
	}
	code, _, errOut = run(t, "", "devices", "get", deviceID, "--site", siteID)
	if code != 0 || errOut != "" || info() != 1 {
		t.Fatalf("cached run: exit %d, stderr %q, info requests %d", code, errOut, info())
	}
}

func TestTableOutput(t *testing.T) {
	setup(t)
	code, out, errOut := run(t, "", "devices", "list", "-o", "table", "--limit", "2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "name state macAddress ipAddress id" {
		t.Fatalf("table:\n%s", out)
	}
	if f := strings.Fields(lines[1]); f[0] != "Device" || f[1] != "0" || f[len(f)-1] != devID(0) {
		t.Fatalf("row %q", lines[1])
	}
	if !strings.Contains(errOut, "showing 2 of 5") {
		t.Errorf("paging note missing: %q", errOut)
	}

	// --fields picks columns, dotted paths through arrays join with commas.
	t.Setenv("UNIFI_OUTPUT", "table")
	_, out, _ = run(t, "", "devices", "list", "--all", "--fields", "id,interfaces.ports.idx")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 6 || !strings.HasSuffix(lines[5], " 1") {
		t.Fatalf("--fields table:\n%s", out)
	}

	// A single object is field/value pairs.
	_, out, _ = run(t, "", "devices", "get", deviceID, "--site", siteID)
	if !strings.HasPrefix(out, "FIELD") || !strings.Contains(out, deviceID) {
		t.Fatalf("object table:\n%s", out)
	}

	code, _, errOut = run(t, "", "devices", "list", "-o", "yaml")
	if code != exitUsage || !strings.Contains(errOut, "json or table") {
		t.Fatalf("bad format: exit %d, %q", code, errOut)
	}
}
