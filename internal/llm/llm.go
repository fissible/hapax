// Package llm provides the deliberately narrow provider seam used by rewrite.
package llm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/fissible/hapax/internal/rewrite"
)

const (
	AnthropicURL     = "https://api.anthropic.com/v1/messages"
	AnthropicVersion = "2023-06-01"
	UserAgent        = "hapax"
	DefaultEndpoint  = "http://127.0.0.1:11434"
)

func Providers() []ProviderID { return []ProviderID{ProviderOllama, ProviderAnthropic} }

type ProviderID string

const (
	ProviderOllama    ProviderID = "ollama"
	ProviderAnthropic ProviderID = "anthropic"
)

type DialFunc func(context.Context, string, string) (net.Conn, error)
type CredentialFactory func(context.Context) (string, error)

type Limits struct{ MaxRequestBytes, MaxResponseBytes int }

func DefaultLimits() Limits {
	return Limits{MaxRequestBytes: 256 << 10, MaxResponseBytes: 1 << 20}
}

type LocalConfig struct {
	Model, Endpoint string
	Limits          Limits
}

func DefaultLocalConfig() LocalConfig {
	return LocalConfig{Endpoint: DefaultEndpoint, Limits: DefaultLimits()}
}

type CloudConfig struct {
	Model     string
	Limits    Limits
	MaxTokens int
}

func DefaultCloudConfig() CloudConfig {
	return CloudConfig{Limits: DefaultLimits(), MaxTokens: 4096}
}

type CloudDeps struct {
	Dial        DialFunc
	Credentials CredentialFactory
	RootCAs     *x509.CertPool
}

var (
	ErrMissingInput     = errors.New("llm is missing required input")
	ErrInvalidConfig    = errors.New("llm configuration is invalid")
	ErrLocalOnly        = errors.New("llm is configured for local-only operation")
	ErrEndpoint         = errors.New("llm endpoint is invalid")
	ErrModeMismatch     = errors.New("llm request mode does not match the provider")
	ErrRequestTooLarge  = errors.New("llm request exceeds the configured size limit")
	ErrResponseTooLarge = errors.New("llm response exceeds the configured size limit")
	ErrRedirect         = errors.New("llm redirects are refused")
	ErrProvider         = errors.New("llm provider failed")
)

type provider struct {
	client      *http.Client
	provider    ProviderID
	model       string
	limits      Limits
	maxTokens   int
	localOnly   bool
	url         string
	credentials CredentialFactory
}

// NewLocal constructs a local provider without opening a socket.
func NewLocal(cfg LocalConfig, dial DialFunc, roots *x509.CertPool) (rewrite.Provider, error) {
	if dial == nil {
		return nil, ErrMissingInput
	}
	if cfg.Model == "" || cfg.Limits.MaxRequestBytes <= 0 || cfg.Limits.MaxResponseBytes <= 0 {
		return nil, ErrInvalidConfig
	}
	if err := validateEndpoint(cfg.Endpoint); err != nil {
		return nil, err
	}
	return newProvider(ProviderOllama, cfg.Model, cfg.Limits, 0, true,
		strings.TrimRight(cfg.Endpoint, "/")+"/api/generate", dial, roots), nil
}

// NewCloud constructs a cloud provider without reading credentials or opening a socket.
func NewCloud(cfg CloudConfig, deps CloudDeps) (rewrite.Provider, error) {
	if deps.Dial == nil || deps.Credentials == nil {
		return nil, ErrMissingInput
	}
	if cfg.Model == "" || cfg.Limits.MaxRequestBytes <= 0 || cfg.Limits.MaxResponseBytes <= 0 || cfg.MaxTokens <= 0 {
		return nil, ErrInvalidConfig
	}
	built := newProvider(ProviderAnthropic, cfg.Model, cfg.Limits, cfg.MaxTokens, false,
		AnthropicURL, deps.Dial, deps.RootCAs)
	built.credentials = deps.Credentials

	return built, nil
}

func newProvider(providerID ProviderID, model string, limits Limits, maxTokens int, localOnly bool, endpoint string, dial DialFunc, roots *x509.CertPool) *provider {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dial(ctx, network, address)
		},
		Proxy:              nil,
		DisableKeepAlives:  true,
		DisableCompression: true,
		TLSClientConfig:    &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS13},
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirect
		},
	}
	return &provider{client: client, provider: providerID, model: model, limits: limits, maxTokens: maxTokens, localOnly: localOnly, url: endpoint}
}

func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ErrEndpoint
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "" {
		return ErrEndpoint
	}
	if _, err := strconv.Atoi(port); err != nil {
		return ErrEndpoint
	}
	ip := net.ParseIP(host)
	if host == "::1" && ip != nil {
		return nil
	}
	if !strings.HasPrefix(host, "127.") || ip == nil || ip.To4() == nil {
		return ErrEndpoint
	}
	return nil
}

func (p *provider) Rewrite(ctx context.Context, request rewrite.RewriteRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if request.LocalOnly != p.localOnly {
		return "", ErrModeMismatch
	}
	body, err := p.body(request.Prompt)
	if err != nil {
		return "", err
	}
	if len(body) > p.limits.MaxRequestBytes {
		return "", ErrRequestTooLarge
	}

	key := ""
	if p.provider == ProviderAnthropic {
		key, err = p.credentials(ctx)
		if err != nil {
			return "", err
		}
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("%w: request: %v", ErrProvider, err)
	}
	httpRequest.Header.Set("content-type", "application/json")
	httpRequest.Header.Set("user-agent", UserAgent)
	if p.provider == ProviderAnthropic {
		httpRequest.Header.Set("x-api-key", key)
		httpRequest.Header.Set("anthropic-version", AnthropicVersion)
	}

	response, err := p.client.Do(httpRequest)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= 300 {
		return "", fmt.Errorf("%w: status %d", ErrProvider, response.StatusCode)
	}
	if response.ContentLength > int64(p.limits.MaxResponseBytes) {
		return "", ErrResponseTooLarge
	}
	reply, err := io.ReadAll(io.LimitReader(response.Body, int64(p.limits.MaxResponseBytes)+1))
	if err != nil {
		return "", err
	}
	if len(reply) > p.limits.MaxResponseBytes {
		return "", ErrResponseTooLarge
	}
	return p.parse(reply)
}

func (p *provider) body(prompt string) ([]byte, error) {
	if p.provider == ProviderOllama {
		return json.Marshal(map[string]any{"model": p.model, "prompt": prompt, "stream": false})
	}
	return json.Marshal(map[string]any{
		"model": p.model, "max_tokens": p.maxTokens,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
}

func (p *provider) parse(reply []byte) (string, error) {
	if p.provider == ProviderOllama {
		var decoded struct {
			Response string `json:"response"`
		}
		if err := json.Unmarshal(reply, &decoded); err != nil || decoded.Response == "" {
			return "", ErrProvider
		}
		return decoded.Response, nil
	}
	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(reply, &decoded); err != nil || len(decoded.Content) == 0 || decoded.Content[0].Type != "text" || decoded.Content[0].Text == "" {
		return "", ErrProvider
	}
	return decoded.Content[0].Text, nil
}
