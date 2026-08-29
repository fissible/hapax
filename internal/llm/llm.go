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

type ProviderID string

const (
	ProviderOllama    ProviderID = "ollama"
	ProviderAnthropic ProviderID = "anthropic"
)

type DialFunc func(context.Context, string, string) (net.Conn, error)
type CredentialFactory func(context.Context) (string, error)

type Deps struct {
	Dial        DialFunc
	Credentials CredentialFactory
	RootCAs     *x509.CertPool
}

type Config struct {
	Provider                                     ProviderID
	Model, LocalEndpoint                         string
	LocalOnly                                    bool
	MaxRequestBytes, MaxResponseBytes, MaxTokens int
}

func DefaultConfig() Config {
	return Config{
		Provider:         ProviderOllama,
		LocalEndpoint:    DefaultEndpoint,
		MaxRequestBytes:  256 << 10,
		MaxResponseBytes: 1 << 20,
		MaxTokens:        4096,
	}
}

var (
	ErrMissingInput     = errors.New("llm missing input")
	ErrInvalidConfig    = errors.New("llm invalid config")
	ErrLocalOnly        = errors.New("llm local-only")
	ErrEndpoint         = errors.New("llm endpoint")
	ErrModeMismatch     = errors.New("llm mode mismatch")
	ErrRequestTooLarge  = errors.New("llm request too large")
	ErrResponseTooLarge = errors.New("llm response too large")
	ErrProvider         = errors.New("llm provider")
)

type provider struct {
	client      *http.Client
	cfg         Config
	url         string
	credentials CredentialFactory
}

// New constructs a provider without reading credentials or opening a socket.
func New(cfg Config, deps Deps) (rewrite.Provider, error) {
	if deps.Dial == nil {
		return nil, ErrMissingInput
	}
	if cfg.Model == "" || cfg.MaxRequestBytes <= 0 || cfg.MaxResponseBytes <= 0 {
		return nil, ErrInvalidConfig
	}

	endpoint := ""
	switch cfg.Provider {
	case ProviderOllama:
		if err := validateEndpoint(cfg.LocalEndpoint); err != nil {
			return nil, err
		}
		endpoint = strings.TrimRight(cfg.LocalEndpoint, "/") + "/api/generate"
	case ProviderAnthropic:
		if cfg.LocalOnly {
			return nil, ErrLocalOnly
		}
		if cfg.LocalEndpoint != "" || cfg.MaxTokens <= 0 {
			return nil, ErrInvalidConfig
		}
		if deps.Credentials == nil {
			return nil, ErrMissingInput
		}
		endpoint = AnthropicURL
	default:
		return nil, ErrInvalidConfig
	}

	transport := &http.Transport{
		DialContext:        deps.Dial,
		Proxy:              nil,
		DisableKeepAlives:  true,
		DisableCompression: true,
		TLSClientConfig:    &tls.Config{RootCAs: deps.RootCAs, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS13},
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirect refused")
		},
	}
	return &provider{client: client, cfg: cfg, url: endpoint, credentials: deps.Credentials}, nil
}

func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ErrEndpoint
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "" || u.Path != "" {
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
	if request.LocalOnly != p.cfg.LocalOnly {
		return "", ErrModeMismatch
	}
	body, err := p.body(request.Prompt)
	if err != nil {
		return "", err
	}
	if len(body) > p.cfg.MaxRequestBytes {
		return "", ErrRequestTooLarge
	}

	key := ""
	if p.cfg.Provider == ProviderAnthropic {
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
	if p.cfg.Provider == ProviderAnthropic {
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
	if response.ContentLength > int64(p.cfg.MaxResponseBytes) {
		return "", ErrResponseTooLarge
	}
	reply, err := io.ReadAll(io.LimitReader(response.Body, int64(p.cfg.MaxResponseBytes)+1))
	if err != nil {
		return "", err
	}
	if len(reply) > p.cfg.MaxResponseBytes {
		return "", ErrResponseTooLarge
	}
	return p.parse(reply)
}

func (p *provider) body(prompt string) ([]byte, error) {
	if p.cfg.Provider == ProviderOllama {
		return json.Marshal(map[string]any{"model": p.cfg.Model, "prompt": prompt, "stream": false})
	}
	return json.Marshal(map[string]any{
		"model": p.cfg.Model, "max_tokens": p.cfg.MaxTokens,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
}

func (p *provider) parse(reply []byte) (string, error) {
	if p.cfg.Provider == ProviderOllama {
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
