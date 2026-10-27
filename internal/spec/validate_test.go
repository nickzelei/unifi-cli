package spec

import (
	"strings"
	"testing"
)

func validate(t *testing.T, cmd, body string) *Validation {
	t.Helper()
	op, n := Find(strings.Fields(cmd))
	if op == nil || n != len(strings.Fields(cmd)) {
		t.Fatalf("no op %q", cmd)
	}
	res, err := Validate(op, []byte(body))
	if err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	return res
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name, cmd, body string
		errs, warns     []string // substrings, each must appear
	}{
		{"valid variant", "dns policies create",
			`{"type":"A_RECORD","enabled":true,"domain":"nas.lan","ipv4Address":"192.168.1.20","ttlSeconds":300}`, nil, nil},
		{"missing discriminator", "dns policies create", `{"enabled":true}`,
			[]string{"$.type: required: one of AAAA_RECORD, A_RECORD"}, nil},
		{"unknown discriminator", "dns policies create", `{"type":"A","enabled":true}`,
			[]string{`$.type: must be one of`, `got "A"`}, nil},
		{"variant fields", "dns policies create", `{"type":"A_RECORD","domain":"x","ttlSeconds":"300","bogus":1}`,
			[]string{"$.enabled: required", "$.ipv4Address: required", `$.ttlSeconds: must be an integer, got string "300"`},
			[]string{"$.bogus: not in the spec"}},
		{"limits", "hotspot vouchers create", `{"count":0,"name":"","timeLimitMinutes":1.5}`,
			[]string{"$.count: must be >= 1", "$.name: must be at least 1", "$.timeLimitMinutes: must be an integer"}, nil},
		{"nested discriminator and unique items", "acl-rules create",
			`{"type":"IPV4","name":"x","action":"ALLOW","enabled":true,"index":1,"protocolFilter":["TCP","TCP"],"enforcingDeviceFilter":{"type":"DEVICES"}}`,
			[]string{"$.enforcingDeviceFilter.deviceIds: required", `$.protocolFilter: items must be unique`}, nil},
		{"null optional", "acl-rules create",
			`{"type":"IPV4","name":"x","action":"ALLOW","enabled":true,"index":1,"protocolFilter":null}`, nil, nil},
		{"enum", "devices action", `{"action":"REBOOT"}`, []string{`$.action: must be one of RESTART, got "REBOOT"`}, nil},
		{"uuid format", "acl-rules create",
			`{"type":"IPV4","name":"x","action":"ALLOW","enabled":true,"index":1,"enforcingDeviceFilter":{"type":"DEVICES","deviceIds":["nope"]}}`,
			[]string{`$.enforcingDeviceFilter.deviceIds[0]: must be a UUID`}, nil},
		{"not an object", "networks create", `[1]`, []string{"$: must be an object, got an array"}, nil},
	}
	for _, c := range cases {
		res := validate(t, c.cmd, c.body)
		check := func(kind string, got, want []string) {
			all := strings.Join(got, "\n")
			for _, w := range want {
				if !strings.Contains(all, w) {
					t.Errorf("%s: %s missing %q; got:\n%s", c.name, kind, w, all)
				}
			}
			if len(want) == 0 && len(got) > 0 {
				t.Errorf("%s: unexpected %s:\n%s", c.name, kind, all)
			}
		}
		check("errors", res.Errors, c.errs)
		check("warnings", res.Warnings, c.warns)
	}
}

// Fields from `get` output (id, metadata) are accepted silently on update,
// and the discriminator value wins over a variant's own (lowercase) enum.
func TestValidateRoundTrip(t *testing.T) {
	res := validate(t, "networks update", `{"id":"x","metadata":{"origin":"USER_DEFINED"},"default":false}`)
	if len(res.Warnings) > 0 {
		t.Errorf("read-only fields warned: %v", res.Warnings)
	}
	res = validate(t, "firewall policies update",
		`{"ipProtocolScope":{"ipVersion":"IPV4_AND_IPV6","protocolFilter":{"type":"NAMED_PROTOCOL","matchOpposite":false,"protocol":{"name":"TCP"}}}}`)
	for _, e := range res.Errors {
		if strings.Contains(e, "protocol") {
			t.Errorf("protocol rejected: %v", res.Errors)
		}
	}
}

// Every op with a body must validate an empty object without error or
// runaway recursion.
func TestValidateEveryBody(t *testing.T) {
	for i := range Ops {
		op := &Ops[i]
		if !op.Body {
			continue
		}
		if _, err := Validate(op, []byte(`{}`)); err != nil {
			t.Errorf("%s: %v", op.Name(), err)
		}
	}
}
