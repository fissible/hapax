package llm_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fissible/hapax/internal/llm"
	"github.com/fissible/hapax/internal/rewrite"
)

// ADR 0007: with --local-only no cloud provider is constructed, no credential
// is read, no dial outside loopback is attempted, no telemetry is emitted —
// asserted by test. Loopback, because the default provider is Ollama on
// localhost: the guarantee is about destination, not silence.

const loopback = "http://127.0.0.1:11434"

func request(prompt string, localOnly bool) rewrite.RewriteRequest {
	return rewrite.RewriteRequest{
		Prompt: prompt, ProfileID: "profile-abc", InvocationID: "invocation-xyz", LocalOnly: localOnly,
	}
}

// ---------------------------------------------------------------------------
// Construction refuses before it can do harm
// ---------------------------------------------------------------------------

// A nil dialer must not fall back to http.DefaultTransport: that is the one
// path to a socket no test supplied, and it would make every other assertion
// here decorative.
// A provider with no way to dial cannot be constructed. The builders elsewhere
// supply the harness's dialer, so this one names the absence explicitly rather
// than going through them — an earlier revision of this file routed it through
// buildLocal/buildCloud and quietly stopped testing anything.
func TestANilDialerIsRefused(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(*harness) (rewrite.Provider, error)
	}{
		{"local", func(h *harness) (rewrite.Provider, error) {
			return llm.NewLocal(localConfig(loopback), nil, nil)
		}},
		{"cloud", func(h *harness) (rewrite.Provider, error) {
			return llm.NewCloud(cloudConfig(), llm.CloudDeps{Credentials: h.credentials})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, plainServer, "127.0.0.1:11434")
			_, err := c.build(h)
			if !errors.Is(err, llm.ErrMissingInput) {
				t.Errorf("error = %v, want ErrMissingInput", err)
			}
			h.nothingLeft(t)
		})
	}
}

// Endpoint parsing is closed. Every rejection here is a spelling that reaches a
// non-loopback destination, or one that defers the question to a resolver.
func TestTheLocalEndpointMustBeALiteralLoopbackAddress(t *testing.T) {
	for _, c := range []struct {
		endpoint string
		ok       bool
	}{
		{"http://127.0.0.1:11434", true},
		{"http://127.1.2.3:11434", true},
		{"http://[::1]:11434", true},
		{"https://127.0.0.1:11434", true},
		{"http://localhost:11434", false},
		{"http://LOCALHOST:11434", false},
		{"http://ollama.internal:11434", false},
		{"http://0.0.0.0:11434", false},
		{"http://[::]:11434", false},
		{"http://126.0.0.1:11434", false},
		{"http://127.0.0.1", false},
		{"http://user@127.0.0.1:11434", false},
		{"http://127.0.0.1:11434?x=1", false},
		{"http://127.0.0.1:11434#f", false},
		{"ftp://127.0.0.1:11434", false},
		{"127.0.0.1:11434", false},
		{"", false},
	} {
		t.Run(c.endpoint, func(t *testing.T) {
			h := newHarness(t, plainServer, "127.0.0.1:11434")
			_, err := h.newLocal(localConfig(c.endpoint))
			if c.ok && err != nil {
				t.Errorf("rejected %q: %v", c.endpoint, err)
			}
			if !c.ok && !errors.Is(err, llm.ErrEndpoint) {
				t.Errorf("accepted %q (err = %v), want ErrEndpoint", c.endpoint, err)
			}
		})
	}
}

// A zero limit meaning "unlimited" is the failure mode worth designing out.
func TestNonPositiveLimitsAreRefused(t *testing.T) {
	for _, c := range []struct {
		name             string
		request, respons int
	}{
		{"zero request", 0, 1 << 20},
		{"negative request", -1, 1 << 20},
		{"zero response", 1 << 10, 0},
		{"negative response", 1 << 10, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, plainServer, "127.0.0.1:11434")
			cfg := localConfig(loopback)
			cfg.Limits.MaxRequestBytes, cfg.Limits.MaxResponseBytes = c.request, c.respons
			if _, err := h.newLocal(cfg); !errors.Is(err, llm.ErrInvalidConfig) {
				t.Errorf("error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

// The byte limits are one policy both arms share, so they are declared once and
// each default configuration is checked against that policy rather than against
// a literal of its own — two copies of the same invariant drift.
func TestDefaultLimitsAreDeclared(t *testing.T) {
	limits := llm.DefaultLimits()
	if limits.MaxRequestBytes != 256<<10 {
		t.Errorf("MaxRequestBytes = %d, want %d", limits.MaxRequestBytes, 256<<10)
	}
	if limits.MaxResponseBytes != 1<<20 {
		t.Errorf("MaxResponseBytes = %d, want %d", limits.MaxResponseBytes, 1<<20)
	}
	if got := llm.DefaultLocalConfig().Limits; got != limits {
		t.Errorf("the local default carries %+v, want the shared %+v", got, limits)
	}
	if got := llm.DefaultCloudConfig().Limits; got != limits {
		t.Errorf("the cloud default carries %+v, want the shared %+v", got, limits)
	}
}

// What the old single default declared, now that each arm owns its own. The
// local endpoint and the token budget were previously asserted together with a
// provider selector and a LocalOnly flag that no longer exist; those two moved
// to the resolver, and these are what is left that a configuration still says.
func TestEachDefaultConfigurationDeclaresItsOwn(t *testing.T) {
	if got := llm.DefaultLocalConfig().Endpoint; got != llm.DefaultEndpoint {
		t.Errorf("the local default endpoint is %q, want %q", got, llm.DefaultEndpoint)
	}
	if got := llm.DefaultCloudConfig().MaxTokens; got != 4096 {
		t.Errorf("the cloud default token budget is %d, want 4096", got)
	}
	// Neither default names a model: a guessed model produces a provider error
	// the user cannot act on, which is why --model is always explicit.
	if got := llm.DefaultLocalConfig().Model; got != "" {
		t.Errorf("the local default names model %q", got)
	}
	if got := llm.DefaultCloudConfig().Model; got != "" {
		t.Errorf("the cloud default names model %q", got)
	}
}

// ---------------------------------------------------------------------------
// The wire contract, decoded rather than searched
// ---------------------------------------------------------------------------

func TestOllamaSendsExactlyTheDeclaredBody(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	provider, err := h.newLocal(localConfig(loopback))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := provider.Rewrite(context.Background(), request("the passage", true))
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if got != "rewritten" {
		t.Errorf("reply = %q, want %q", got, "rewritten")
	}

	req, body := h.onlyRequest(t)
	if req.Method != http.MethodPost {
		t.Errorf("method = %s", req.Method)
	}
	if req.URL.Path != "/api/generate" {
		t.Errorf("path = %q, want /api/generate", req.URL.Path)
	}
	if ct := req.Header.Get("content-type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	if want := []string{"model", "prompt", "stream"}; !reflect.DeepEqual(keys(body), want) {
		t.Errorf("body keys = %v, want exactly %v", keys(body), want)
	}
	if body["prompt"] != "the passage" {
		t.Errorf("prompt = %v", body["prompt"])
	}
	if body["model"] != "llama3" {
		t.Errorf("model = %v, want the configured model", body["model"])
	}
	if body["stream"] != false {
		t.Errorf("stream = %v, want false", body["stream"])
	}
}

func TestAnthropicSendsExactlyTheDeclaredBody(t *testing.T) {
	h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
	provider, err := h.newCloud(cloudConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := provider.Rewrite(context.Background(), request("the passage", false))
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if got != "rewritten" {
		t.Errorf("reply = %q", got)
	}

	req, body := h.onlyRequest(t)
	if req.Method != http.MethodPost || req.URL.Path != "/v1/messages" {
		t.Errorf("%s %s, want POST /v1/messages", req.Method, req.URL.Path)
	}
	if req.Host != "api.anthropic.com" {
		t.Errorf("Host = %q, want api.anthropic.com", req.Host)
	}
	if got := req.Header.Get("x-api-key"); got != "test-key" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if want := []string{"max_tokens", "messages", "model"}; !reflect.DeepEqual(keys(body), want) {
		t.Errorf("body keys = %v, want exactly %v", keys(body), want)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %v, want one element", body["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0] = %v", messages[0])
	}
	if want := []string{"content", "role"}; !reflect.DeepEqual(keys(message), want) {
		t.Errorf("message keys = %v, want exactly %v", keys(message), want)
	}
	if message["role"] != "user" || message["content"] != "the passage" {
		t.Errorf("message = %v", message)
	}
	if body["model"] != "claude-sonnet-5" {
		t.Errorf("model = %v, want the configured model", body["model"])
	}
	if body["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v, want the declared default 4096", body["max_tokens"])
	}
}

// The configured model reaches the wire on both paths, not a hard-coded one —
// so this configures a model no default and no fixture uses, and looks for that.
// Routing it through the shared builders made it assert a string it no longer
// set, which an implementation hard-coding that string would have satisfied.
func TestTheConfiguredModelIsSent(t *testing.T) {
	for _, p := range []struct {
		name      string
		kind      serverKind
		addr      string
		reply     string
		build     func(*harness) (rewrite.Provider, error)
		localOnly bool
	}{
		{"ollama", plainServer, "127.0.0.1:11434", `{"response":"rewritten"}`,
			withLocal(func(c *llm.LocalConfig) { c.Model = "a-specific-model" }), true},
		{"anthropic", cloudCertServer, "api.anthropic.com:443", `{"content":[{"type":"text","text":"rewritten"}]}`,
			withCloud(func(c *llm.CloudConfig) { c.Model = "a-specific-model" }), false},
	} {
		t.Run(p.name, func(t *testing.T) {
			h := newHarness(t, p.kind, p.addr)
			provider, err := p.build(h)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := provider.Rewrite(context.Background(), request("p", p.localOnly)); err != nil {
				t.Fatalf("Rewrite: %v", err)
			}
			_, body := h.onlyRequest(t)
			if body["model"] != "a-specific-model" {
				t.Errorf("model = %v, want the configured one", body["model"])
			}
		})
	}
}

// A non-positive token budget is refused for the same reason the byte limits
// are: zero must not quietly mean "whatever the provider decides".
func TestANonPositiveTokenBudgetIsRefused(t *testing.T) {
	for _, tokens := range []int{0, -1} {
		t.Run(itoa(tokens), func(t *testing.T) {
			h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
			cfg := cloudConfig()
			cfg.MaxTokens = tokens
			if _, err := h.newCloud(cfg); !errors.Is(err, llm.ErrInvalidConfig) {
				t.Errorf("error = %v, want ErrInvalidConfig", err)
			}
			h.nothingLeft(t)
		})
	}
}

func TestTheTokenBudgetIsConfigurable(t *testing.T) {
	h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
	cfg := cloudConfig()
	cfg.MaxTokens = 1234
	provider, err := h.newCloud(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", false)); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	_, body := h.onlyRequest(t)
	if body["max_tokens"] != float64(1234) {
		t.Errorf("max_tokens = %v, want 1234", body["max_tokens"])
	}
}

// The dialled address is the pinned host, not merely the Host header — a header
// can be set on a request going anywhere.
func TestTheCloudHostIsPinned(t *testing.T) {
	h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
	provider, err := h.newCloud(cloudConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", false)); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if want := []string{"api.anthropic.com:443"}; !reflect.DeepEqual(h.addresses(), want) {
		t.Errorf("dialled %v, want %v", h.addresses(), want)
	}
}

// "No identity fields", not "no identifier values": the assertions above pin
// the key sets, and these pin the places a field could hide outside the body.
func TestNoIdentityFieldsLeaveTheProcess(t *testing.T) {
	for _, c := range []struct {
		name  string
		tls   bool
		build func(*harness) (rewrite.Provider, error)
		req   rewrite.RewriteRequest
	}{
		{"ollama", false, buildLocal(loopback), request("p", true)},
		{"anthropic", true, buildCloud(), request("p", false)},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, tlsKind(c.tls), "127.0.0.1:11434", "api.anthropic.com:443")
			provider, err := c.build(h)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := provider.Rewrite(context.Background(), c.req); err != nil {
				t.Fatalf("Rewrite: %v", err)
			}

			req, _ := h.onlyRequest(t)
			for _, secret := range []string{c.req.ProfileID, c.req.InvocationID} {
				if strings.Contains(req.URL.String(), secret) {
					t.Errorf("%q appears in the URL %q", secret, req.URL)
				}
				for name, values := range req.Header {
					for _, value := range values {
						if strings.Contains(value, secret) {
							t.Errorf("%q appears in header %s", secret, name)
						}
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The mode is the boundary, not the request field
// ---------------------------------------------------------------------------

// RewriteRequest.LocalOnly is a per-call boolean and cannot be a security
// control. A provider refuses a request that disagrees with the mode it was
// built in rather than honouring the field either way.
func TestARequestDisagreeingWithTheModeIsRefused(t *testing.T) {
	for _, c := range []struct {
		name    string
		tls     bool
		build   func(*harness) (rewrite.Provider, error)
		reqFlag bool
	}{
		{"local provider, cloud request", false, buildLocal(loopback), false},
		{"cloud provider, local request", true, buildCloud(), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, tlsKind(c.tls), "127.0.0.1:11434", "api.anthropic.com:443")
			provider, err := c.build(h)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := provider.Rewrite(context.Background(), request("p", c.reqFlag)); !errors.Is(err, llm.ErrModeMismatch) {
				t.Errorf("error = %v, want ErrModeMismatch", err)
			}
			h.nothingLeft(t)
		})
	}
}

// ---------------------------------------------------------------------------
// One exchange, no fallback, no telemetry
// ---------------------------------------------------------------------------

// Keep-alives are disabled, so one dial per request: the exchange count is
// observable at the dial seam and not only at the client.
func TestOneRewriteIsOneDialAndOneRequest(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	provider, err := h.newLocal(localConfig(loopback))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := provider.Rewrite(context.Background(), request("p", true)); err != nil {
			t.Fatalf("Rewrite %d: %v", i, err)
		}
	}
	// One REQUEST per Rewrite is the contract — a transparent retry would show
	// up here as a fourth. The dial count is a consequence of disabling
	// keep-alives, so it is bounded rather than pinned.
	// Keep-alives being disabled is part of the contract and the source guard
	// enforces it, so the dial count is exact: an implementation that re-enables
	// reuse after construction would show fewer than three.
	if dials, requests, _ := h.counts(); requests != 3 || dials != 3 {
		t.Errorf("dials=%d requests=%d after three rewrites, want 3 and 3", dials, requests)
	}
}

// A cloud failure is a hard error. Asserted at the destination level: no dial
// to the local endpoint ever happens, so a fallback would have to show up here.
func TestACloudFailureNeverFallsBackToLocal(t *testing.T) {
	h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
	h.handler = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	provider, err := h.newCloud(cloudConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := provider.Rewrite(context.Background(), request("p", false))
	if err == nil {
		t.Fatalf("accepted a 500, returning %q", got)
	}
	if got != "" {
		t.Errorf("returned %q alongside an error", got)
	}
	if want := []string{"api.anthropic.com:443"}; !reflect.DeepEqual(h.addresses(), want) {
		t.Errorf("dialled %v, want only the cloud host", h.addresses())
	}
}

// A redirect is not followed: the transport refuses it rather than reaching a
// second destination the caller never authorised.
func TestARedirectIsNotFollowed(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	h.handler = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/elsewhere", http.StatusFound)
	}
	provider, err := h.newLocal(localConfig(loopback))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", true)); err == nil {
		t.Fatal("followed or accepted a redirect")
	}
	if dials, requests, _ := h.counts(); dials != 1 || requests != 1 {
		t.Errorf("dials=%d requests=%d, want 1 and 1", dials, requests)
	}
	for _, addr := range h.addresses() {
		if !strings.HasPrefix(addr, "127.0.0.1:") {
			t.Errorf("dialled %q after a redirect", addr)
		}
	}
}

// Compression off, so the response bound counts the same bytes on the wire and
// after decoding.
func TestCompressionIsDisabled(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	provider, err := h.newLocal(localConfig(loopback))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", true)); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	req, _ := h.onlyRequest(t)
	if got := req.Header.Get("accept-encoding"); strings.Contains(got, "gzip") {
		t.Errorf("accept-encoding = %q; compression must be disabled so the response bound counts wire bytes", got)
	}
}

// ---------------------------------------------------------------------------
// Limits
// ---------------------------------------------------------------------------

// Over the limit costs nothing: no credential read and no dial.
func TestAnOversizedRequestIsRefusedBeforeAnythingLeaves(t *testing.T) {
	h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
	cfg := cloudConfig()
	cfg.Limits.MaxRequestBytes = 200
	provider, err := h.newCloud(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := provider.Rewrite(context.Background(), request(strings.Repeat("x", 500), false)); !errors.Is(err, llm.ErrRequestTooLarge) {
		t.Errorf("error = %v, want ErrRequestTooLarge", err)
	}
	if dials, requests, creds := h.counts(); dials != 0 || requests != 0 || creds != 0 {
		t.Errorf("dials=%d requests=%d credentialCalls=%d, want all zero", dials, requests, creds)
	}
}

// The limit is on the serialized body, so the boundary is expressed in terms of
// what actually goes on the wire rather than the prompt's length.
func TestTheRequestLimitIsTheSerializedBody(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	cfg := localConfig(loopback)
	cfg.Limits.MaxRequestBytes = 1 << 20
	provider, err := h.newLocal(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", true)); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	_, body := h.onlyRequest(t)
	if body["prompt"] != "p" {
		t.Fatalf("prompt = %v", body["prompt"])
	}

	// Now set the limit to one byte under what that body serialized to.
	h2 := newHarness(t, plainServer, "127.0.0.1:11434")
	cfg.Limits.MaxRequestBytes = len(h.bodies[0]) - 1
	provider, err = llm.NewLocal(cfg, h2.dial, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", true)); !errors.Is(err, llm.ErrRequestTooLarge) {
		t.Errorf("a body of %d bytes passed a limit of %d: %v", len(h.bodies[0]), cfg.Limits.MaxRequestBytes, err)
	}
	h2.nothingLeft(t)

	// And exactly at the limit it must pass.
	h3 := newHarness(t, plainServer, "127.0.0.1:11434")
	cfg.Limits.MaxRequestBytes = len(h.bodies[0])
	provider, err = llm.NewLocal(cfg, h3.dial, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", true)); err != nil {
		t.Errorf("a body of exactly the limit was refused: %v", err)
	}
}

// Both shapes: a declared Content-Length over the limit, and a chunked body
// with no declared length at all.
func TestAnOversizedResponseIsRefused(t *testing.T) {
	for _, c := range []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
	}{
		{"declared content-length", func(w http.ResponseWriter, r *http.Request) {
			payload := `{"response":"` + strings.Repeat("y", 5000) + `"}`
			w.Header().Set("content-length", itoa(len(payload)))
			_, _ = w.Write([]byte(payload))
		}},
		{"chunked", func(w http.ResponseWriter, r *http.Request) {
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte(`{"response":"`))
			for i := 0; i < 50; i++ {
				_, _ = w.Write([]byte(strings.Repeat("y", 200)))
				if flusher != nil {
					flusher.Flush()
				}
			}
			_, _ = w.Write([]byte(`"}`))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, plainServer, "127.0.0.1:11434")
			h.handler = c.handler
			cfg := localConfig(loopback)
			cfg.Limits.MaxResponseBytes = 1000
			provider, err := h.newLocal(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := provider.Rewrite(context.Background(), request("p", true)); !errors.Is(err, llm.ErrResponseTooLarge) {
				t.Errorf("error = %v, want ErrResponseTooLarge", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Replies
// ---------------------------------------------------------------------------

func TestAnUnusableReplyIsAnError(t *testing.T) {
	for _, c := range []struct {
		name, body string
		tls        bool
		build      func(*harness) (rewrite.Provider, error)
		localOnly  bool
	}{
		{"ollama empty", `{"response":""}`, false, buildLocal(loopback), true},
		{"ollama missing", `{}`, false, buildLocal(loopback), true},
		{"ollama not json", `not json`, false, buildLocal(loopback), true},
		{"anthropic empty content", `{"content":[]}`, true, buildCloud(), false},
		{"anthropic wrong type", `{"content":[{"type":"tool_use","text":"x"}]}`, true, buildCloud(), false},
		{"anthropic empty text", `{"content":[{"type":"text","text":""}]}`, true, buildCloud(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, tlsKind(c.tls), "127.0.0.1:11434", "api.anthropic.com:443")
			h.handler = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(c.body)) }
			provider, err := c.build(h)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, err := provider.Rewrite(context.Background(), request("p", c.localOnly))
			if err == nil {
				t.Fatalf("accepted %q, returning %q", c.body, got)
			}
			if got != "" {
				t.Errorf("returned %q alongside an error", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Context and credentials
// ---------------------------------------------------------------------------

func TestACancelledContextStopsBeforeDialling(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	provider, err := h.newLocal(localConfig(loopback))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := provider.Rewrite(ctx, request("p", true)); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	h.nothingLeft(t)
}

// A credential failure is returned rather than degraded into an unauthenticated
// request.
func TestACredentialFailureIsAnErrorAndNothingIsSent(t *testing.T) {
	h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
	h.credErr = errors.New("no key configured")
	provider, err := h.newCloud(cloudConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", false)); err == nil {
		t.Fatal("accepted a credential failure")
	}
	if dials, requests, creds := h.counts(); dials != 0 || requests != 0 || creds != 1 {
		t.Errorf("dials=%d requests=%d credentialCalls=%d, want 0, 0, 1", dials, requests, creds)
	}
}

// Local mode never reads a credential, even across many rewrites.
func TestLocalModeNeverReadsACredential(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	provider, err := h.newLocal(localConfig(loopback))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := provider.Rewrite(context.Background(), request("p", true)); err != nil {
			t.Fatalf("Rewrite %d: %v", i, err)
		}
	}
	if _, _, creds := h.counts(); creds != 0 {
		t.Errorf("credential factory called %d times in local mode", creds)
	}
}

// The provider satisfies the interface rewrite declares — the whole point of
// the component.
func TestItSatisfiesTheRewriteProviderInterface(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	provider, err := h.newLocal(localConfig(loopback))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var _ rewrite.Provider = provider
}

// ---------------------------------------------------------------------------
// The remaining egress paths
// ---------------------------------------------------------------------------

// TLS is verified against the pinned name, not skipped. The certificate's SAN
// is api.anthropic.com and the SNI the server sees must be the same — an
// implementation that sets InsecureSkipVerify or overrides ServerName is
// talking to whatever answered, which is the failure this pinning exists to
// prevent.
func TestTheCloudPathVerifiesThePinnedName(t *testing.T) {
	h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
	provider, err := h.newCloud(cloudConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", false)); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if want := []string{"api.anthropic.com"}; !reflect.DeepEqual(h.sni, want) {
		t.Errorf("SNI = %v, want %v", h.sni, want)
	}
}

// A proxy in the environment must not become a destination. The dialer's
// allowlist has only the pinned host, so a proxy dial fails the test.
func TestAProxyInTheEnvironmentIsIgnored(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:9")

	h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
	provider, err := h.newCloud(cloudConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", false)); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if want := []string{"api.anthropic.com:443"}; !reflect.DeepEqual(h.addresses(), want) {
		t.Errorf("dialled %v, want only the pinned host", h.addresses())
	}
}

// Every non-success status is an error, decided before the body is read as a
// reply. Testing only 500 would let a 401 whose body happens to parse become a
// rewrite.
func TestEveryNonSuccessStatusIsAnError(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(itoa(status), func(t *testing.T) {
			h := newHarness(t, plainServer, "127.0.0.1:11434")
			h.handler = func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				// A body that would decode perfectly well as a success.
				_, _ = w.Write([]byte(`{"response":"looks like a rewrite"}`))
			}
			provider, err := h.newLocal(localConfig(loopback))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, err := provider.Rewrite(context.Background(), request("p", true))
			if err == nil {
				t.Fatalf("status %d accepted, returning %q", status, got)
			}
			if got != "" {
				t.Errorf("returned %q alongside an error", got)
			}
			// A failure is one exchange too: no retry loop behind the error.
			if _, requests, _ := h.counts(); requests != 1 {
				t.Errorf("%d requests for one failed rewrite, want 1", requests)
			}
		})
	}
}

// The outbound header set is declared, so an identity-bearing header cannot be
// added under a name the value-scanning test would never think to look at.
func TestTheOutboundHeaderSetIsDeclared(t *testing.T) {
	for _, c := range []struct {
		name      string
		tls       bool
		build     func(*harness) (rewrite.Provider, error)
		localOnly bool
		want      []string
	}{
		{"ollama", false, buildLocal(loopback), true, []string{"Content-Type", "User-Agent"}},
		{"anthropic", true, buildCloud(), false, []string{"Anthropic-Version", "Content-Type", "User-Agent", "X-Api-Key"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, tlsKind(c.tls), "127.0.0.1:11434", "api.anthropic.com:443")
			provider, err := c.build(h)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := provider.Rewrite(context.Background(), request("p", c.localOnly)); err != nil {
				t.Fatalf("Rewrite: %v", err)
			}
			req, _ := h.onlyRequest(t)
			if got := h.applicationHeaders(req); !reflect.DeepEqual(got, c.want) {
				t.Errorf("application headers = %v, want exactly %v", got, c.want)
			}
			if ua := req.Header.Get("user-agent"); ua != "hapax" {
				t.Errorf("user-agent = %q, want the fixed %q — the Go default leaks a version", ua, "hapax")
			}
		})
	}
}

// The response bound must stop the read, not merely notice afterwards. This
// server streams without end; an implementation that buffers to completion
// never returns.
func TestAnUnboundedResponseIsCutOffNotBuffered(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	written := make(chan int, 1)
	h.handler = func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(`{"response":"`))
		total := 13
		chunk := strings.Repeat("y", 1024)
		for i := 0; i < 4096; i++ {
			n, err := w.Write([]byte(chunk))
			total += n
			if err != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		written <- total
	}

	cfg := localConfig(loopback)
	cfg.Limits.MaxResponseBytes = 4096
	provider, err := h.newLocal(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := provider.Rewrite(context.Background(), request("p", true))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, llm.ErrResponseTooLarge) {
			t.Errorf("error = %v, want ErrResponseTooLarge", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Rewrite did not return; the response is being buffered to completion rather than bounded")
	}
}

// ---------------------------------------------------------------------------
// The seam is the only way out — asserted about the source, not the run
// ---------------------------------------------------------------------------

// A runtime harness cannot observe a path it never exercises. This one parses
// the package's own non-test source. Substring matching was not enough — it
// misses a zero-value http.Client, a transport without the injected dialer, and
// an aliased import — so the rules are structural.
func TestThePackageCannotReachAroundTheSeam(t *testing.T) {
	forbidden := map[string]string{
		"http.DefaultClient":    "a client the test never supplied",
		"http.DefaultTransport": "a transport the test never supplied",
		"http.Get":              "bypasses the injected dialer",
		"http.Post":             "bypasses the injected dialer",
		"http.PostForm":         "bypasses the injected dialer",
		"http.Head":             "bypasses the injected dialer",
		"net.Dial":              "bypasses the injected dialer",
		"net.DialTimeout":       "bypasses the injected dialer",
		"net.Dialer":            "bypasses the injected dialer",
		"os.Getenv":             "the mode is resolved at the composition root, not here",
		"os.LookupEnv":          "the mode is resolved at the composition root, not here",
		"os.Environ":            "the mode is resolved at the composition root, not here",
	}
	// Every field the design requires to be set explicitly on the one transport
	// this package constructs.
	// Presence is not enough: Proxy: nil is the CORRECT value while
	// DialContext: nil is the bug. So each field declares what it must be.
	// "" means any non-nil value.
	requiredTransportFields := map[string]string{
		"DialContext": "", "Proxy": "nil", "DisableKeepAlives": "true",
		"DisableCompression": "true", "TLSClientConfig": "",
	}
	requiredClientFields := map[string]string{"Transport": "", "CheckRedirect": ""}

	// The package may import only these. Anything else could dial on its behalf,
	// which no selector rule inside this package would ever see.
	permittedImports := map[string]bool{
		`"context"`: true, `"crypto/tls"`: true, `"crypto/x509"`: true, `"encoding/json"`: true,
		`"errors"`: true, `"fmt"`: true, `"io"`: true, `"net"`: true, `"net/http"`: true,
		`"net/url"`: true, `"strconv"`: true, `"strings"`: true, `"time"`: true,
		`"github.com/fissible/hapax/internal/rewrite"`: true,
	}
	// net and crypto/tls carry many ways to open a socket. Rather than list the
	// dial APIs and miss one — net.DialTCP, tls.Dial, and the rest — only the
	// non-dialling names this package actually needs are allowed through.
	permittedNet := map[string]bool{
		"Conn": true, "IP": true, "ParseIP": true, "SplitHostPort": true, "JoinHostPort": true,
	}
	permittedTLS := map[string]bool{"Config": true, "VersionTLS12": true, "VersionTLS13": true}

	set := token.NewFileSet()
	pkgs, err := parser.ParseDir(set, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}

	// Every function name this package defines, so a policy field referring to
	// one can be shown to resolve here rather than to an imported nil.
	defined := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
					defined[fn.Name.Name] = true
				}
			}
		}
	}

	policyFields := map[string]bool{
		"DialContext": true, "Proxy": true, "DisableKeepAlives": true,
		"DisableCompression": true, "TLSClientConfig": true, "Transport": true, "CheckRedirect": true,
	}

	var files, transports, clients int
	literalTypes := map[token.Pos]bool{}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			files++

			// An aliased import would defeat every selector rule below.
			for _, imp := range file.Imports {
				if imp.Name != nil && imp.Name.Name != "_" {
					t.Errorf("%s aliases import %s as %q; the seam rules match on package names", name, imp.Path.Value, imp.Name.Name)
				}
				if !permittedImports[imp.Path.Value] {
					t.Errorf("%s imports %s, which is outside the declared set — it could dial on this package's behalf", name, imp.Path.Value)
				}
			}

			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.SelectorExpr:
					ident, ok := node.X.(*ast.Ident)
					if !ok {
						return true
					}
					if why, bad := forbidden[ident.Name+"."+node.Sel.Name]; bad {
						t.Errorf("%s uses %s.%s — %s", name, ident.Name, node.Sel.Name, why)
					}
					if ident.Name == "net" && !permittedNet[node.Sel.Name] {
						t.Errorf("%s uses net.%s; only %v are permitted, because every other name in net can open a socket", name, node.Sel.Name, keysOf(permittedNet))
					}
					if ident.Name == "tls" && !permittedTLS[node.Sel.Name] {
						t.Errorf("%s uses tls.%s; only %v are permitted, because tls.Dial bypasses the injected dialer", name, node.Sel.Name, keysOf(permittedTLS))
					}
				case *ast.CallExpr:
					// new(http.Client) sidesteps every field rule below.
					if fn, ok := node.Fun.(*ast.Ident); ok && fn.Name == "new" && len(node.Args) == 1 {
						if sel, ok := node.Args[0].(*ast.SelectorExpr); ok {
							if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" &&
								(sel.Sel.Name == "Client" || sel.Sel.Name == "Transport") {
								t.Errorf("%s uses new(http.%s); a zero value inherits the default transport", name, sel.Sel.Name)
							}
						}
					}
				case *ast.AssignStmt:
					// A conforming literal mutated afterwards is the same hole.
					for _, target := range node.Lhs {
						sel, ok := target.(*ast.SelectorExpr)
						if ok && policyFields[sel.Sel.Name] {
							t.Errorf("%s assigns to %s after construction; the outbound policy must be fixed at construction", name, sel.Sel.Name)
						}
					}
				case *ast.TypeSpec:
					// A defined type based on http.Client evades every rule below.
					if sel, ok := node.Type.(*ast.SelectorExpr); ok {
						if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" &&
							(sel.Sel.Name == "Client" || sel.Sel.Name == "Transport") {
							t.Errorf("%s defines a type based on http.%s, which sidesteps the construction rules", name, sel.Sel.Name)
						}
					}
				case *ast.ValueSpec:
					// var c http.Client does the same, without a literal.
					if sel, ok := node.Type.(*ast.SelectorExpr); ok {
						if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" &&
							(sel.Sel.Name == "Client" || sel.Sel.Name == "Transport") && len(node.Values) == 0 {
							t.Errorf("%s declares a zero-valued http.%s; it inherits the default transport", name, sel.Sel.Name)
						}
					}
				case *ast.CompositeLit:
					selector, ok := node.Type.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					ident, ok := selector.X.(*ast.Ident)
					if !ok || ident.Name != "http" {
						return true
					}
					values := map[string]string{}
					for _, element := range node.Elts {
						kv, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := kv.Key.(*ast.Ident)
						if !ok {
							continue
						}
						rendered := new(strings.Builder)
						if err := printer.Fprint(rendered, set, kv.Value); err != nil {
							t.Fatalf("rendering %s: %v", key.Name, err)
						}
						values[key.Name] = rendered.String()
					}
					check := func(kind string, required map[string]string) {
						for field, want := range required {
							got, present := values[field]
							if !present {
								t.Errorf("%s constructs an http.%s without %s; every outbound policy must be explicit", name, kind, field)
								continue
							}
							if want == "" && got == "nil" {
								t.Errorf("%s sets http.%s.%s to nil", name, kind, field)
							}
							if want != "" && got != want {
								t.Errorf("%s sets http.%s.%s to %s, want %s", name, kind, field, got, want)
							}
						}
					}
					switch selector.Sel.Name {
					case "Transport":
						transports++
						literalTypes[selector.Pos()] = true
						check("Transport", requiredTransportFields)
					case "Client":
						clients++
						literalTypes[selector.Pos()] = true
						check("Client", requiredClientFields)
					}
					// A policy function must be an inline literal or a function
					// defined in this package — DialContext may additionally be
					// the injected dialer itself, and nothing else. A selector
					// naming some other package's identifier could be nil.
					for field, extra := range map[string]string{"CheckRedirect": "", "DialContext": "deps.Dial"} {
						value, present := values[field]
						if !present || strings.HasPrefix(value, "func(") || defined[value] || (extra != "" && value == extra) {
							continue
						}
						t.Errorf("%s sets %s to %q; it must be an inline literal, a function defined in this package%s",
							name, field, value, map[bool]string{true: ", or deps.Dial", false: ""}[extra != ""])
					}
				}
				return true
			})
		}
	}

	// http.Client and http.Transport may appear only as the type of the one
	// checked literal each, or behind a pointer. A VALUE occurrence anywhere
	// else — a struct field, a parameter, a variable — is a zero value that
	// would inherit the default transport; a pointer has to be assigned from
	// somewhere, and the only sources are the literals above and new(), which
	// is already refused.
	pointerTypes := map[token.Pos]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				if star, ok := n.(*ast.StarExpr); ok {
					if sel, ok := star.X.(*ast.SelectorExpr); ok {
						pointerTypes[sel.Pos()] = true
					}
				}
				return true
			})
		}
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || literalTypes[sel.Pos()] || pointerTypes[sel.Pos()] {
					return true
				}
				if pkgIdent, ok := sel.X.(*ast.Ident); ok && pkgIdent.Name == "http" &&
					(sel.Sel.Name == "Client" || sel.Sel.Name == "Transport") {
					t.Errorf("%s names http.%s by value outside the one construction site; a zero value there inherits the default transport",
						name, sel.Sel.Name)
				}
				return true
			})
		}
	}

	if files == 0 {
		t.Fatal("no non-test source was scanned; this guard is vacuous")
	}
	if transports != 1 || clients != 1 {
		t.Errorf("scanned %d files and found %d http.Transport and %d http.Client literals; the package must construct EXACTLY the one client it owns", files, transports, clients)
	}
}

// ---------------------------------------------------------------------------
// The negative half of the TLS assertion
// ---------------------------------------------------------------------------

// A trusted certificate also succeeds under InsecureSkipVerify, so the positive
// test alone cannot tell verification from bypass. With roots that do not trust
// the server, the handshake must fail.
func TestAnUntrustedCloudCertificateIsRefused(t *testing.T) {
	h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
	deps := h.cloudDeps()
	deps.RootCAs = x509.NewCertPool() // trusts nothing
	provider, err := llm.NewCloud(cloudConfig(), deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = provider.Rewrite(context.Background(), request("p", false))
	if err == nil {
		t.Fatal("accepted a certificate signed by an untrusted root")
	}
	// It must fail at VERIFICATION, having dialled — an implementation that
	// rejected an empty pool early could otherwise skip verification whenever
	// the pool is non-empty.
	var unknown x509.UnknownAuthorityError
	var verification *tls.CertificateVerificationError
	if !errors.As(err, &unknown) && !errors.As(err, &verification) {
		t.Errorf("error = %v, want a certificate verification failure", err)
	}
	if want := []string{"api.anthropic.com:443"}; !reflect.DeepEqual(h.addresses(), want) {
		t.Errorf("dialled %v, want the pinned host — verification must happen on the wire", h.addresses())
	}
	if _, requests, _ := h.counts(); requests != 0 {
		t.Errorf("%d requests reached the server despite a failed handshake", requests)
	}
}

// ---------------------------------------------------------------------------
// Refusals that must not have done anything first
// ---------------------------------------------------------------------------

func TestConstructionRefusalsHaveNoSideEffects(t *testing.T) {
	for _, c := range []struct {
		name  string
		build func(*harness) (rewrite.Provider, error)
	}{
		{"bad endpoint", buildLocal("http://example.com:11434")},
		{"zero request limit", withLocal(func(c *llm.LocalConfig) { c.Limits.MaxRequestBytes = 0 })},
		{"zero response limit", withLocal(func(c *llm.LocalConfig) { c.Limits.MaxResponseBytes = 0 })},
		{"empty local model", withLocal(func(c *llm.LocalConfig) { c.Model = "" })},
		{"empty cloud model", withCloud(func(c *llm.CloudConfig) { c.Model = "" })},
		// "unknown provider" is not a row here any more: there is no provider
		// field to make unknown. It is a resolver refusal now, in
		// internal/workflow, asserted with neither arm running.
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, plainServer, "127.0.0.1:11434")
			if _, err := c.build(h); err == nil {
				t.Fatal("accepted")
			}
			h.nothingLeft(t)
		})
	}
}

// ---------------------------------------------------------------------------
// The cloud path gets the same scrutiny as the local one
// ---------------------------------------------------------------------------

func TestEveryNonSuccessStatusIsAnErrorOnTheCloudPathToo(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(itoa(status), func(t *testing.T) {
			h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
			h.handler = func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"looks like a rewrite"}]}`))
			}
			provider, err := h.newCloud(cloudConfig())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, err := provider.Rewrite(context.Background(), request("p", false))
			if err == nil {
				t.Fatalf("status %d accepted, returning %q", status, got)
			}
			if got != "" {
				t.Errorf("returned %q alongside an error", got)
			}
			if _, requests, _ := h.counts(); requests != 1 {
				t.Errorf("%d requests for one failed rewrite, want 1", requests)
			}
		})
	}
}

// Sampling statuses leaves 404, 408, 418 and 503 conforming. The contract is
// the 2xx class, so the boundary is what gets tested.
func TestSuccessIsTheTwoHundredClass(t *testing.T) {
	for _, p := range providers {
		for _, c := range []struct {
			status int
			ok     bool
		}{{200, true}, {299, true}, {300, false}} {
			// 199 is deliberately absent. Measured: a handler writing 199
			// delivers 200 to the client, because Go's stack treats 1xx as
			// informational and sends the implicit final status after it. A
			// sub-200 final status cannot reach a client through a real server,
			// so asserting it would test the harness rather than the provider.
			t.Run(p.name+"/"+itoa(c.status), func(t *testing.T) {
				h := newHarness(t, p.kind, p.addr)
				h.handler = func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(c.status)
					_, _ = w.Write([]byte(p.reply))
				}
				provider, err := p.build(h)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				got, err := provider.Rewrite(context.Background(), request("p", p.localOnly))
				if c.ok {
					if err != nil {
						t.Errorf("status %d rejected: %v", c.status, err)
					}
					if got != "rewritten" {
						t.Errorf("status %d returned %q, want %q", c.status, got, "rewritten")
					}
					return
				}
				if err == nil {
					t.Errorf("status %d accepted, returning %q", c.status, got)
				}
				if got != "" {
					t.Errorf("status %d returned %q alongside an error", c.status, got)
				}
			})
		}
	}
}

// (3) The response bound and the malformed-body rules apply to the cloud path
// too; testing them locally only leaves a cloud implementation free to buffer.
func TestTheCloudResponseIsBoundedAndParsed(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
		h.handler = func(w http.ResponseWriter, r *http.Request) {
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"`))
			for i := 0; i < 4096; i++ {
				if _, err := w.Write([]byte(strings.Repeat("y", 1024))); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
		cfg := cloudConfig()
		cfg.Limits.MaxResponseBytes = 4096
		provider, err := h.newCloud(cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := provider.Rewrite(context.Background(), request("p", false))
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, llm.ErrResponseTooLarge) {
				t.Errorf("error = %v, want ErrResponseTooLarge", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the cloud response was buffered to completion rather than bounded")
		}
	})

	t.Run("not json", func(t *testing.T) {
		h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
		h.handler = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<html>nope</html>`)) }
		provider, err := h.newCloud(cloudConfig())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if got, err := provider.Rewrite(context.Background(), request("p", false)); err == nil {
			t.Fatalf("accepted a non-JSON reply, returning %q", got)
		}
	})
}

// (4) The status decides before the body is read. This server flushes a failing
// header and then never finishes the body: an implementation that reads first
// hangs.
func TestANonSuccessStatusIsDecidedBeforeReadingTheBody(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			h := newHarness(t, p.kind, p.addr)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			h.handler = func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}
			provider, err := p.build(h)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := provider.Rewrite(context.Background(), request("p", p.localOnly))
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Error("accepted a 500")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the body was read before the status was decided")
			}
		})
	}
}

// Cancellation on the cloud path must be checked before the credential is read,
// not only before the dial.
func TestACancelledContextStopsBeforeReadingACredential(t *testing.T) {
	h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
	provider, err := h.newCloud(cloudConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := provider.Rewrite(ctx, request("p", false)); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	h.nothingLeft(t)
}

// And a request already in flight is cancelled rather than run to completion.
// The handler signals that it was entered, so a provider returning
// context.Canceled without ever making a request cannot pass.
func TestAnInFlightRequestIsCancelled(t *testing.T) {
	for _, c := range []struct {
		name      string
		kind      serverKind
		build     func(*harness) (rewrite.Provider, error)
		addr      string
		localOnly bool
	}{
		{"ollama", plainServer, buildLocal(loopback), "127.0.0.1:11434", true},
		{"anthropic", cloudCertServer, buildCloud(), "api.anthropic.com:443", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.kind, c.addr)
			started := make(chan struct{})
			release := make(chan struct{})
			serverSaw := make(chan struct{})
			var once sync.Once
			h.handler = func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() { close(started) })
				// The request's OWN context must end. A provider that issues the
				// call with a detached context and merely returns early on the
				// caller's cancellation leaves this transfer running.
				select {
				case <-r.Context().Done():
					close(serverSaw)
				case <-release:
				}
			}
			t.Cleanup(func() { close(release) })

			provider, err := c.build(h)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, err := provider.Rewrite(ctx, request("p", c.localOnly))
				done <- err
			}()

			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("the request never reached the server")
			}
			cancel()

			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("error = %v, want context.Canceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Rewrite ignored cancellation of an in-flight request")
			}
			select {
			case <-serverSaw:
			case <-time.After(5 * time.Second):
				t.Error("the server never saw its request cancelled; the call was issued with a detached context")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Remaining leaks and shapes
// ---------------------------------------------------------------------------

// Trailers are metadata the header and value scans would never see.
func TestNoTrailersAreSent(t *testing.T) {
	h := newHarness(t, plainServer, "127.0.0.1:11434")
	provider, err := h.newLocal(localConfig(loopback))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", true)); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	req, _ := h.onlyRequest(t)
	if len(req.Trailer) != 0 {
		t.Errorf("request carried trailers: %v", req.Trailer)
	}
}

// A single Decode accepts a valid document followed by anything at all. The
// wire response is one JSON document or it is an error.
func TestAReplyFollowedByGarbageIsRefused(t *testing.T) {
	for _, c := range []struct {
		name, body string
		tls        bool
		build      func(*harness) (rewrite.Provider, error)
		localOnly  bool
	}{
		{"ollama", `{"response":"rewritten"}{"response":"and more"}`, false, buildLocal(loopback), true},
		{"anthropic", `{"content":[{"type":"text","text":"rewritten"}]} trailing garbage`, true, buildCloud(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, tlsKind(c.tls), "127.0.0.1:11434", "api.anthropic.com:443")
			h.handler = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(c.body)) }
			provider, err := c.build(h)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got, err := provider.Rewrite(context.Background(), request("p", c.localOnly)); err == nil {
				t.Fatalf("accepted a reply with trailing content, returning %q", got)
			}
		})
	}
}

// https to a loopback address is declared valid, so it must actually work —
// against a certificate whose IP SAN is 127.0.0.1.
func TestAnHTTPSLoopbackEndpointWorks(t *testing.T) {
	h := newHarness(t, loopbackCertServer, "127.0.0.1:11434")
	provider, err := h.newLocal(localConfig("https://127.0.0.1:11434"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := provider.Rewrite(context.Background(), request("p", true))
	if err != nil {
		t.Fatalf("https loopback rewrite failed: %v", err)
	}
	if got != "rewritten" {
		t.Errorf("reply = %q", got)
	}
	if want := []string{"127.0.0.1:11434"}; !reflect.DeepEqual(h.addresses(), want) {
		t.Errorf("dialled %v, want %v", h.addresses(), want)
	}
}

// And the name is still checked on that path: a certificate for the cloud host
// does not authenticate a loopback endpoint.
func TestAnHTTPSLoopbackEndpointStillVerifiesTheName(t *testing.T) {
	h := newHarness(t, cloudCertServer, "127.0.0.1:11434")
	provider, err := h.newLocal(localConfig("https://127.0.0.1:11434"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.Rewrite(context.Background(), request("p", true)); err == nil {
		t.Error("accepted a certificate that does not name 127.0.0.1")
	}
}

// The cloud path needs a credential factory; the local path must not, since it
// never calls one. Accepting nil on the cloud path would defer the failure to a
// panic or an unauthenticated request.
func TestTheCloudPathRequiresACredentialFactory(t *testing.T) {
	t.Run("cloud refuses nil", func(t *testing.T) {
		h := newHarness(t, cloudCertServer, "api.anthropic.com:443")
		deps := h.cloudDeps()
		deps.Credentials = nil
		if _, err := llm.NewCloud(cloudConfig(), deps); !errors.Is(err, llm.ErrMissingInput) {
			t.Errorf("error = %v, want ErrMissingInput", err)
		}
		h.nothingLeft(t)
	})

	// The local half of this test used to set Credentials to nil and check the
	// local path did not mind. There is no field to nil now: NewLocal has no
	// parameter that could carry one, which is the whole point, so what is left
	// to assert is that it constructs and rewrites without one existing at all.
	t.Run("local needs none", func(t *testing.T) {
		h := newHarness(t, plainServer, "127.0.0.1:11434")
		provider, err := h.newLocal(localConfig(loopback))
		if err != nil {
			t.Fatalf("local construction failed without a credential: %v", err)
		}
		if _, err := provider.Rewrite(context.Background(), request("p", true)); err != nil {
			t.Errorf("Rewrite: %v", err)
		}
	})
}
