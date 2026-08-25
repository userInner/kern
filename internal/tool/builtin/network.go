package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/tool"
)

const (
	maxNetworkBody = 2 << 20
	maxRedirects   = 3
)

var networkSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["url"],
  "properties": {
    "url": {"type": "string", "description": "Public HTTP or HTTPS URL"}
  }
}`)

// Network performs bounded public HTTP GET requests with SSRF defenses.
type Network struct {
	client *http.Client
}

// NewNetwork constructs the network-read tool.
func NewNetwork() *Network {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parsing network target: %w", err)
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolving network target: %w", err)
		}
		for _, address := range addresses {
			if !isPublicIP(address.IP) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
		}
		return nil, errors.New("tool: network target does not resolve to a public address")
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("tool: too many redirects")
		}
		return validatePublicURL(request.URL)
	}
	return &Network{client: client}
}

func (n *Network) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "network",
		Description: "Fetch a bounded public HTTP/HTTPS resource. Local, private, link-local, " +
			"credential-bearing, and redirect-escaped targets are blocked.",
		InputSchema: networkSchema,
	}
}

func (n *Network) Effect(input json.RawMessage) (operation.Effect, error) {
	_, err := decodeNetwork(input)
	return operation.EffectNetworkRead, err
}

func (n *Network) Execute(
	ctx context.Context,
	input json.RawMessage,
) (tool.Result, error) {
	request, err := decodeNetwork(input)
	if err != nil {
		return tool.Result{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, request.URL.String(), nil)
	if err != nil {
		return tool.Result{}, fmt.Errorf("creating network request: %w", err)
	}
	httpRequest.Header.Set("Accept", "text/plain, text/html, application/json, application/xml;q=0.9, */*;q=0.1")
	httpRequest.Header.Set("User-Agent", "Kern-Core/0.1")
	response, err := n.client.Do(httpRequest)
	if err != nil {
		return tool.Result{}, fmt.Errorf("fetching network resource: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxNetworkBody+1))
	if err != nil {
		return tool.Result{}, fmt.Errorf("reading network resource: %w", err)
	}
	if len(body) > maxNetworkBody {
		return tool.Result{}, errors.New("tool: network response exceeds size limit")
	}
	encoded, err := json.Marshal(map[string]any{
		"url":          response.Request.URL.String(),
		"status":       response.StatusCode,
		"content_type": response.Header.Get("Content-Type"),
		"body":         string(body),
		"trusted":      false,
	})
	if err != nil {
		return tool.Result{}, fmt.Errorf("encoding network result: %w", err)
	}
	return tool.Result{Content: string(encoded)}, nil
}

type networkInput struct {
	URL *url.URL
}

func decodeNetwork(input json.RawMessage) (networkInput, error) {
	var raw struct {
		URL string `json:"url"`
	}
	if err := decodeStrict(input, &raw); err != nil {
		return networkInput{}, err
	}
	parsed, err := url.Parse(raw.URL)
	if err != nil {
		return networkInput{}, fmt.Errorf("%w: invalid url", tool.ErrInvalidInput)
	}
	if err := validatePublicURL(parsed); err != nil {
		return networkInput{}, err
	}
	return networkInput{URL: parsed}, nil
}

func validatePublicURL(target *url.URL) error {
	if target.Scheme != "http" && target.Scheme != "https" {
		return fmt.Errorf("%w: url must use http or https", tool.ErrInvalidInput)
	}
	if target.Hostname() == "" || target.User != nil {
		return fmt.Errorf("%w: url host is invalid", tool.ErrInvalidInput)
	}
	if err := validateNetworkPort(target); err != nil {
		return err
	}
	host := strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("tool: local network targets are blocked")
	}
	if ip := net.ParseIP(host); ip != nil && !isPublicIP(ip) {
		return errors.New("tool: private network targets are blocked")
	}
	return nil
}

func validateNetworkPort(target *url.URL) error {
	port := target.Port()
	if port == "" {
		return nil
	}
	if (target.Scheme == "http" && port == "80") || (target.Scheme == "https" && port == "443") {
		return nil
	}
	return errors.New("tool: non-standard network ports are blocked")
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

var _ tool.Handler = (*Network)(nil)
