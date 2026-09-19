package setuptui

import (
	"errors"
	"net/url"
	"strings"
)

// errHTTPRejected is the S7 field-level rejection: plain http:// is never
// accepted for any URL the wizard collects. TLS 1.3 minimum is a product
// invariant and the wizard is a product surface.
var errHTTPRejected = errors.New("https required — tls 1.3 is a product invariant")

// validateStoreAddress enforces S7 for the secret-store address in BOTH the
// TUI and the plain/accessible paths (S12: the sequential fallback keeps
// every security property). It accepts exactly one shape: a parseable URL
// with the https scheme and a non-empty host.
func validateStoreAddress(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return errors.New("address is required")
	}
	u, err := url.Parse(addr)
	if err != nil {
		return errors.New("address is not a valid URL")
	}
	// url.Parse lowercases the scheme, so HTTP:// and Http:// land here too.
	if u.Scheme == "http" {
		return errHTTPRejected
	}
	if u.Scheme != "https" {
		return errors.New("address must start with https://")
	}
	if u.Host == "" {
		return errors.New("address must include a host")
	}
	return nil
}

// networkPolicyValues is the exact vocabulary installer.Config accepts for
// NetworkPolicy. The TUI presents friendlier labels; the value written to
// the config and printed by --dry-mode must be one of these, or the
// equivalent `oberth install` command is invalid.
var networkPolicyValues = map[string]string{
	"strict": "true", // display label → installer value
	"auto":   "auto",
	"off":    "false",
	// Already-canonical values map to themselves so state round-trips.
	"true":  "true",
	"false": "false",
}

// canonicalNetworkPolicy maps a wizard/plain-mode answer to the installer's
// auto|true|false vocabulary. ok is false for anything else.
func canonicalNetworkPolicy(v string) (string, bool) {
	c, ok := networkPolicyValues[strings.TrimSpace(v)]
	return c, ok
}
