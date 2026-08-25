package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	requestTimeout  = 30 * time.Second
	maxResponseSize = 8 << 20
	maxErrorBody    = 4 << 10
)

type Config struct {
	BaseURL      string
	Token        string
	TokenCommand string
	CACert       string
}

func FromEnv() Config {
	return Config{
		BaseURL:      strings.TrimSpace(os.Getenv("OBERTH_BASE_URL")),
		Token:        strings.TrimSpace(os.Getenv("OBERTH_TOKEN")),
		TokenCommand: strings.TrimSpace(os.Getenv("OBERTH_TOKEN_COMMAND")),
		CACert:       strings.TrimSpace(os.Getenv("OBERTH_CA_CERT")),
	}
}

func (config Config) Configured() bool { return config.BaseURL != "" }

func (config Config) resolveToken(ctx context.Context) (string, error) {
	if config.Token != "" {
		return config.Token, nil
	}
	if config.TokenCommand == "" {
		return "", errors.New("client: set OBERTH_TOKEN, or OBERTH_TOKEN_COMMAND naming a command that prints one")
	}
	command := exec.CommandContext(ctx, "/bin/sh", "-c", config.TokenCommand) // #nosec G204 -- the operator's own command, from their environment.
	var stderr strings.Builder
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("client: OBERTH_TOKEN_COMMAND failed: %s", detail)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("client: OBERTH_TOKEN_COMMAND printed nothing")
	}
	return token, nil
}

type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

func New(ctx context.Context, config Config) (*Client, error) {
	if config.BaseURL == "" {
		return nil, errors.New("client: set OBERTH_BASE_URL to the server's https address")
	}
	base, err := url.Parse(config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("client: OBERTH_BASE_URL is not a URL: %w", err)
	}
	if base.Scheme != "https" && base.Scheme != "http" {
		return nil, fmt.Errorf("client: OBERTH_BASE_URL must be http or https, got %q", base.Scheme)
	}
	if base.Host == "" {
		return nil, errors.New("client: OBERTH_BASE_URL has no host")
	}
	token, err := config.resolveToken(ctx)
	if err != nil {
		return nil, err
	}
	transport, err := newTransport(config.CACert)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:  base,
		token: token,
		http:  &http.Client{Timeout: requestTimeout, Transport: transport},
	}, nil
}

func newTransport(caCert string) (http.RoundTripper, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport, nil
	}
	cloned := transport.Clone()
	if caCert == "" {
		return cloned, nil
	}
	pem, err := os.ReadFile(caCert) // #nosec G304 -- operator-supplied trust anchor path.
	if err != nil {
		return nil, fmt.Errorf("client: read OBERTH_CA_CERT: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("client: OBERTH_CA_CERT %s contains no certificate", caCert)
	}
	cloned.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return cloned, nil
}

func (client *Client) Get(ctx context.Context, path string, query map[string]string, into any) error {
	target := *client.base
	target.Path = strings.TrimRight(target.Path, "/") + path
	if len(query) > 0 {
		values := url.Values{}
		for name, value := range query {
			if value != "" {
				values.Set(name, value)
			}
		}
		target.RawQuery = values.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fmt.Errorf("client: build request for %s: %w", path, err)
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Accept", "application/json")

	response, err := client.http.Do(request)
	if err != nil {
		return classifyTransport(path, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return statusError(path, response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("client: read %s: %w", path, err)
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("client: %s returned a body that is not the expected JSON", path)
	}
	return nil
}

func (client *Client) GetRaw(ctx context.Context, path string, query map[string]string) ([]byte, error) {
	var raw json.RawMessage
	if err := client.Get(ctx, path, query, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func classifyTransport(path string, err error) error {
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return fmt.Errorf("client: the server's certificate does not cover %q; it is issued for %s. "+
			"Reach the server by one of those names, or have the operator add this address to the certificate",
			hostname.Host, strings.Join(certificateNames(hostname.Certificate), ", "))
	}
	var authority x509.UnknownAuthorityError
	if errors.As(err, &authority) {
		return fmt.Errorf("client: the server's certificate is signed by an authority this machine "+
			"does not trust; set OBERTH_CA_CERT to the PEM that signed it (%s)", path)
	}
	if strings.Contains(err.Error(), "x509") || strings.Contains(err.Error(), "certificate") {
		return fmt.Errorf("client: the server's certificate could not be verified for %s", path)
	}
	return fmt.Errorf("client: cannot reach the server for %s", path)
}

func certificateNames(certificate *x509.Certificate) []string {
	if certificate == nil {
		return []string{"an unreadable set of names"}
	}
	names := append([]string{}, certificate.DNSNames...)
	for _, address := range certificate.IPAddresses {
		names = append(names, address.String())
	}
	if len(names) == 0 && certificate.Subject.CommonName != "" {
		names = append(names, certificate.Subject.CommonName)
	}
	return names
}

func statusError(path string, response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	detail := ""
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		detail = strings.TrimSpace(payload.Error)
	}
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("client: the server rejected the token; check OBERTH_TOKEN or OBERTH_TOKEN_COMMAND (%s)", detail)
	case http.StatusForbidden:
		return fmt.Errorf("client: not permitted: %s", detail)
	case http.StatusNotFound:
		return fmt.Errorf("client: not found: %s (%s)", path, detail)
	}
	if response.StatusCode >= 500 {
		return fmt.Errorf("client: the server failed with %d on %s", response.StatusCode, path)
	}
	return fmt.Errorf("client: %s returned %d: %s", path, response.StatusCode, detail)
}
