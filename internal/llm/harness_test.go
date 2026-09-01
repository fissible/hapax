package llm_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/rewrite"
)

// Headers the transport owns. Everything else in an outbound request is the
// application's and must be in the provider's declared set.
var transportHeaders = map[string]bool{
	"Content-Length": true, "Accept-Encoding": true, "Connection": true,
}

// harness is the no-egress test rig. The dialer ENFORCES an allowlist: an
// unapproved address fails the test immediately rather than being recorded and
// connected anyway, which is what "the harness fails on any dial outside
// loopback" has to mean. Everything runs through the real http.Transport, so
// redirects, keep-alives, compression and chunked encoding are exercised rather
// than faked.
type harness struct {
	t *testing.T

	mu       sync.Mutex
	dials    []string
	requests []*http.Request
	bodies   []string
	sni      []string
	credLog  int

	allowed map[string]bool
	kind    serverKind
	server  *httptest.Server
	handler func(w http.ResponseWriter, r *http.Request)
	credErr error
	readCap int64
}

// serverKind picks the certificate the test server presents, so a test can
// exercise the pinned cloud name and a loopback name separately.
type serverKind int

const (
	plainServer serverKind = iota
	cloudCertServer
	loopbackCertServer
)

func newHarness(t *testing.T, kind serverKind, allow ...string) *harness {
	t.Helper()
	h := &harness{t: t, allowed: map[string]bool{}, readCap: 1 << 22}
	for _, a := range allow {
		h.allowed[a] = true
	}

	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read to EOF, bounded: a client could send valid JSON first and append
		// more in later chunks, which a single Read would never see.
		body, err := io.ReadAll(io.LimitReader(r.Body, h.readCap))
		if err != nil {
			h.t.Errorf("reading request body: %v", err)
		}
		h.mu.Lock()
		h.requests = append(h.requests, r.Clone(context.Background()))
		h.bodies = append(h.bodies, string(body))
		if r.TLS != nil {
			h.sni = append(h.sni, r.TLS.ServerName)
		}
		handler := h.handler
		h.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"response":"rewritten","content":[{"type":"text","text":"rewritten"}]}`))
	})

	h.kind = kind
	if kind == plainServer {
		h.server = httptest.NewServer(mux)
	} else {
		h.server = httptest.NewUnstartedServer(mux)
		h.server.TLS = &tls.Config{Certificates: []tls.Certificate{certificates(t)[kind].pair}}
		h.server.StartTLS()
	}
	t.Cleanup(h.server.Close)
	return h
}

// dial fails the test on any address outside the allowlist, then connects to
// the local server. A wrong destination is a failure, not a note.
func (h *harness) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	h.mu.Lock()
	h.dials = append(h.dials, addr)
	permitted := h.allowed[addr]
	h.mu.Unlock()
	if !permitted {
		h.t.Errorf("dialled %q, which is not in this test's allowlist", addr)
		return nil, fmt.Errorf("harness refuses %s", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, h.server.Listener.Addr().String())
}

func (h *harness) credentials(context.Context) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.credLog++
	return "test-key", h.credErr
}

// roots trusts whatever this harness's server presents.
func (h *harness) roots() *x509.CertPool {
	pool := x509.NewCertPool()
	if h.kind != plainServer {
		pool.AddCert(certificates(h.t)[h.kind].leaf)
	}
	return pool
}

// newLocal and newCloud replace a single New(cfg, Deps). The boundary this
// slice is about is that a local construction has nowhere to put a credential:
// NewLocal takes a dialer and a root pool and no credential of any kind, so no
// caller can supply one — now or after a later edit — and no import allowlist
// has to be maintained to keep that true.
//
// The refactor is deliberately compiler-checked rather than textual. Each call
// site below picks newLocal or newCloud by the type of its configuration, and
// choosing wrongly fails to build rather than passing quietly, which is what a
// find-and-replace over forty-eight sites would otherwise risk.
func (h *harness) newLocal(cfg llm.LocalConfig) (rewrite.Provider, error) {
	return llm.NewLocal(cfg, h.dial, h.rootsOrNil())
}

func (h *harness) newCloud(cfg llm.CloudConfig) (rewrite.Provider, error) {
	return llm.NewCloud(cfg, h.cloudDeps())
}

func (h *harness) cloudDeps() llm.CloudDeps {
	return llm.CloudDeps{Dial: h.dial, Credentials: h.credentials, RootCAs: h.rootsOrNil()}
}

func (h *harness) rootsOrNil() *x509.CertPool {
	if h.kind == plainServer {
		return nil
	}
	return h.roots()
}

func (h *harness) counts() (dials, requests, creds int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.dials), len(h.requests), h.credLog
}

// nothingLeft asserts the three things that must not have happened before a
// refusal: no dial, no request, no credential read.
func (h *harness) nothingLeft(t *testing.T) {
	t.Helper()
	if dials, requests, creds := h.counts(); dials != 0 || requests != 0 || creds != 0 {
		t.Errorf("dials=%d requests=%d credentialCalls=%d, want all zero", dials, requests, creds)
	}
}

func (h *harness) addresses() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.dials...)
}

func (h *harness) onlyRequest(t *testing.T) (*http.Request, map[string]any) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requests) != 1 {
		t.Fatalf("got %d requests, want exactly 1", len(h.requests))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(h.bodies[0]), &body); err != nil {
		t.Fatalf("request body is not JSON: %v (%q)", err, h.bodies[0])
	}
	return h.requests[0], body
}

// applicationHeaders returns the outbound header names the application chose,
// with the transport's own removed.
func (h *harness) applicationHeaders(r *http.Request) []string {
	out := make([]string, 0, len(r.Header))
	for name := range r.Header {
		if !transportHeaders[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func itoa(v int) string { return strconv.Itoa(v) }

// ---------------------------------------------------------------------------
// A certificate for the pinned host
// ---------------------------------------------------------------------------

type authority struct {
	leaf *x509.Certificate
	pair tls.Certificate
}

var (
	certOnce sync.Once
	certVal  map[serverKind]*authority
)

// certificates mints one certificate naming the pinned cloud host and one
// naming loopback, so both paths can be exercised with real verification.
// InsecureSkipVerify and a ServerName override are deliberately unused: either
// would test a different program.
func certificates(t *testing.T) map[serverKind]*authority {
	t.Helper()
	certOnce.Do(func() {
		certVal = map[serverKind]*authority{
			cloudCertServer:    mint(t, []string{"api.anthropic.com"}, nil),
			loopbackCertServer: mint(t, nil, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}),
		}
	})
	return certVal
}

func mint(t *testing.T, dnsNames []string, ips []net.IP) *authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	name := "loopback"
	if len(dnsNames) > 0 {
		name = dnsNames[0]
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              dnsNames,
		IPAddresses:           ips,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return &authority{leaf: leaf, pair: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}}
}

// ---------------------------------------------------------------------------
// Configurations
// ---------------------------------------------------------------------------

// The two configurations are distinct types rather than one struct with a
// provider selector: NewLocal must not be handed a cloud choice either, and a
// shared Config with a Provider field would let it be.
func localConfig(endpoint string) llm.LocalConfig {
	cfg := llm.DefaultLocalConfig()
	cfg.Model, cfg.Endpoint = "llama3", endpoint
	return cfg
}

func cloudConfig() llm.CloudConfig {
	cfg := llm.DefaultCloudConfig()
	cfg.Model = "claude-sonnet-5"
	return cfg
}

// endpointAddress is the host:port a local endpoint should dial.
func endpointAddress(t *testing.T, endpoint string) string {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse %q: %v", endpoint, err)
	}
	return u.Host
}

var _ = strings.Contains

func tlsKind(cloud bool) serverKind {
	if cloud {
		return cloudCertServer
	}
	return plainServer
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// providers is the pair every contract that applies to both is tabled over.
var providers = []struct {
	name  string
	kind  serverKind
	addr  string
	reply string
	build func(*harness) (rewrite.Provider, error)
	// localOnly is the mode a request to this arm must declare. It used to be
	// read off the configuration; a provider no longer carries one, because the
	// mode is resolved once above and the provider is chosen from it.
	localOnly bool
}{
	{"ollama", plainServer, "127.0.0.1:11434", `{"response":"rewritten"}`, buildLocal("http://127.0.0.1:11434"), true},
	{"anthropic", cloudCertServer, "api.anthropic.com:443", `{"content":[{"type":"text","text":"rewritten"}]}`, buildCloud(), false},
}

// buildLocal and buildCloud are how a table row names an arm. The tables used
// to carry a `cfg llm.Config` column, which is exactly what let one row hold a
// local configuration and the next a cloud one — a heterogeneous shape this
// slice removes from the production types and should not leave standing in the
// harness.
func buildLocal(endpoint string) func(*harness) (rewrite.Provider, error) {
	return func(h *harness) (rewrite.Provider, error) { return h.newLocal(localConfig(endpoint)) }
}

func buildCloud() func(*harness) (rewrite.Provider, error) {
	return func(h *harness) (rewrite.Provider, error) { return h.newCloud(cloudConfig()) }
}

// withLocal and withCloud build a deliberately invalid configuration for one
// arm. They exist so a refusal table cannot hold a heterogeneous config: each
// row names the arm it is about.
func withLocal(mutate func(*llm.LocalConfig)) func(*harness) (rewrite.Provider, error) {
	return func(h *harness) (rewrite.Provider, error) {
		cfg := localConfig(loopback)
		mutate(&cfg)
		return h.newLocal(cfg)
	}
}

func withCloud(mutate func(*llm.CloudConfig)) func(*harness) (rewrite.Provider, error) {
	return func(h *harness) (rewrite.Provider, error) {
		cfg := cloudConfig()
		mutate(&cfg)
		return h.newCloud(cfg)
	}
}
