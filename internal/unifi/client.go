package unifi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// UserAgent is sent with every request.
var UserAgent = "unifi-cli"

// Client talks to a UniFi console. The API key is resolved lazily on the
// first request, so commands that never hit the network don't need it.
type Client struct {
	profile Profile
	base    string
	key     string
	hc      *http.Client
}

// APIError is a non-2xx response.
type APIError struct {
	Method, URL string
	Status      int
	Body        []byte
}

// Error prints the console's error code and message when the body is the
// standard error JSON, else the raw body.
func (e *APIError) Error() string {
	var std struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(e.Body))
	if json.Unmarshal(e.Body, &std) == nil && std.Message != "" {
		msg = std.Message
		if std.Code != "" {
			msg = std.Code + ": " + msg
		}
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("HTTP %d %s (%s %s)", e.Status, msg, e.Method, e.URL)
}

// ReadOnlyError is a non-GET request refused because the profile is read-only.
type ReadOnlyError struct{ Method, URL string }

func (e *ReadOnlyError) Error() string {
	return fmt.Sprintf("read-only: refusing %s %s (read_only is set in the profile or UNIFI_READ_ONLY)", e.Method, e.URL)
}

// NewClient builds a client for the profile.
func NewClient(p Profile, timeout time.Duration) (*Client, error) {
	base, err := p.Base()
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig, err = TLSConfig(p)
	if err != nil {
		return nil, err
	}
	return &Client{profile: p, base: base, hc: &http.Client{Transport: tr, Timeout: timeout}}, nil
}

// Base returns the integration API base URL.
func (c *Client) Base() string { return c.base }

// URL joins a spec path (e.g. "/v1/sites") and query onto the base URL.
func (c *Client) URL(path string, q url.Values) string {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// Do sends a request to a spec path. It returns the body of a 2xx response,
// or an *APIError.
func (c *Client) Do(ctx context.Context, method, path string, q url.Values, body []byte) ([]byte, error) {
	return c.DoURL(ctx, method, c.URL(path, q), body)
}

// DoURL sends a request to an absolute URL on the console. A read-only
// profile allows only GET.
func (c *Client) DoURL(ctx context.Context, method, u string, body []byte) ([]byte, error) {
	if c.profile.ReadOnly && method != http.MethodGet {
		return nil, &ReadOnlyError{Method: method, URL: u}
	}
	if c.key == "" {
		k, err := c.profile.ResolveAPIKey(ctx)
		if err != nil {
			return nil, err
		}
		c.key = k
	}
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, explainTLS(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &APIError{Method: method, URL: u, Status: resp.StatusCode, Body: out}
	}
	return out, nil
}

// TLSConfig returns the TLS settings for a profile: pinned leaf certificate
// when a fingerprint is set, no verification when insecure, else system roots.
func TLSConfig(p Profile) (*tls.Config, error) {
	if p.Insecure {
		return &tls.Config{InsecureSkipVerify: true}, nil //nolint:gosec // explicit opt-in
	}
	if p.TLSFingerprint == "" {
		return &tls.Config{}, nil
	}
	want, err := NormalizeFingerprint(p.TLSFingerprint)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		// Chain verification is replaced by pinning the exact leaf certificate.
		InsecureSkipVerify: true, //nolint:gosec // verified in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("tls: no peer certificate")
			}
			if got := Fingerprint(cs.PeerCertificates[0]); got != want {
				return fmt.Errorf("tls: certificate fingerprint mismatch: got %s, pinned %s (re-run `unifi config init` if the console certificate changed)", got, want)
			}
			return nil
		},
	}, nil
}

// Fingerprint is the lowercase hex SHA-256 of a certificate's DER bytes.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// NormalizeFingerprint accepts "sha256:AB:CD..." style input and returns lowercase hex.
func NormalizeFingerprint(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "sha256:")
	s = strings.ReplaceAll(s, ":", "")
	if b, err := hex.DecodeString(s); err != nil || len(b) != sha256.Size {
		return "", fmt.Errorf("invalid tls fingerprint %q: want 64 hex characters (SHA-256)", s)
	}
	return s, nil
}

// ProbeCertificate connects to the console and returns its leaf certificate
// and whether it verifies against the system roots.
func ProbeCertificate(ctx context.Context, p Profile) (*x509.Certificate, bool, error) {
	origin, err := p.Origin()
	if err != nil {
		return nil, false, err
	}
	u, err := url.Parse(origin)
	if err != nil {
		return nil, false, err
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // inspection only
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, false, err
	}
	defer conn.Close()
	cs := conn.(*tls.Conn).ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return nil, false, errors.New("console presented no certificate")
	}
	leaf := cs.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{DNSName: u.Hostname(), Intermediates: inter})
	return leaf, verr == nil, nil
}

func explainTLS(err error) error {
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	if errors.As(err, &ua) || errors.As(err, &hn) {
		return fmt.Errorf("%w\nhint: the console uses a self-signed certificate; run `unifi config init` to pin it", err)
	}
	return err
}
