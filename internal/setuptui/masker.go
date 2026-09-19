package setuptui

import "strings"

// masker implements allowlist-shaped secret masking (S3). The wizard knows
// every secret it holds; values are registered with the masker before first
// use. Pattern-matching output is explicitly avoided — only known handles
// are masked.
type masker struct {
	secrets map[string]struct{}
}

func newMasker() *masker {
	return &masker{secrets: make(map[string]struct{})}
}

// register adds a secret value to the masker. Empty values are ignored.
func (m *masker) register(secret string) {
	if secret == "" {
		return
	}
	m.secrets[secret] = struct{}{}
}

// mask replaces all registered secret values in s with "********".
func (m *masker) mask(s string) string {
	for secret := range m.secrets {
		s = strings.ReplaceAll(s, secret, "********")
	}
	return s
}
