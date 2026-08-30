package localbao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxResponseBytes bounds a response body. The store is local and its answers
// are small; an unbounded read is a way for a wedged process to take the
// command down with it.
const maxResponseBytes = 4 << 20

// call issues one OpenBao API request. Every write below goes through here so
// there is one place that knows about the token header and one place that
// decides what an error looks like.
func (options Options) call(ctx context.Context, method, path, token string, body any, into any) (int, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(options.Address, "/")+path, payload)
	if err != nil {
		return 0, err
	}
	if token != "" {
		request.Header.Set("X-Vault-Token", token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := options.HTTP.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _, _ = io.Copy(io.Discard, response.Body); _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return response.StatusCode, err
	}
	if response.StatusCode >= 400 {
		// The body carries OpenBao's own "errors" array, which is the only
		// useful thing about a 400 here.
		return response.StatusCode, fmt.Errorf("openbao %s %s: %s: %s",
			method, path, response.Status, strings.TrimSpace(string(raw)))
	}
	if into != nil && len(bytes.TrimSpace(raw)) != 0 {
		if err := json.Unmarshal(raw, into); err != nil {
			return response.StatusCode, fmt.Errorf("openbao %s %s: decode response: %w", method, path, err)
		}
	}
	return response.StatusCode, nil
}

// Policy is the tier boundary, written out so it can be read.
//
// The CI tier reaches the upstream subtree and nothing else. The release tier
// reaches the whole KV mount. A CI identity asking for a release path is
// refused by this policy, inside OpenBao, which is the property the local
// profile exists to keep rather than reimplement.
func ciPolicy(kvMount string) string {
	return fmt.Sprintf("path %q {\n  capabilities = [\"read\"]\n}\n", kvMount+"/data/upstream/*")
}

func releasePolicy(kvMount string) string {
	return fmt.Sprintf("path %q {\n  capabilities = [\"read\"]\n}\n", kvMount+"/data/*")
}

// configure brings the auth mount, the KV mount, the policies and the roles to
// the state the server expects. Every step tolerates already being done.
func (options Options) configure(ctx context.Context, root string, publicKey []byte) error {
	if err := options.ensureAuthMount(ctx, root); err != nil {
		return err
	}
	if _, err := options.call(ctx, http.MethodPost, "/v1/auth/"+options.JWTMount+"/config", root, map[string]any{
		"jwt_validation_pubkeys": []string{string(publicKey)},
		// The issuer is bound so a token minted by something else that happens
		// to hold the same key cannot log in here.
		"bound_issuer": Issuer,
	}, nil); err != nil {
		return fmt.Errorf("localbao: configure the jwt auth mount: %w", err)
	}
	options.say("openbao: jwt auth mount %q trusts the run-identity signing key", options.JWTMount)

	if err := options.ensureKVMount(ctx, root); err != nil {
		return err
	}
	for _, tier := range []struct {
		role, policy, body string
	}{
		{options.CIRole, options.CIRole, ciPolicy(options.KVMount)},
		{options.ReleaseRole, options.ReleaseRole, releasePolicy(options.KVMount)},
	} {
		if _, err := options.call(ctx, http.MethodPut, "/v1/sys/policies/acl/"+tier.policy, root,
			map[string]any{"policy": tier.body}, nil); err != nil {
			return fmt.Errorf("localbao: write the %s policy: %w", tier.policy, err)
		}
		if _, err := options.call(ctx, http.MethodPost, "/v1/auth/"+options.JWTMount+"/role/"+tier.role, root, map[string]any{
			"role_type": "jwt",
			// The binding. A role accepts exactly one subject, which is the
			// analogue of a kubernetes role's bound_service_account_names.
			"bound_subject":   tier.role,
			"bound_audiences": []string{Audience},
			"user_claim":      "sub",
			"token_policies":  []string{tier.policy},
			"token_ttl":       RoleTokenTTLSeconds,
			"token_max_ttl":   RoleTokenTTLSeconds,
		}, nil); err != nil {
			return fmt.Errorf("localbao: write the %s role: %w", tier.role, err)
		}
		options.say("openbao: role %q bound to subject %q with policy %q", tier.role, tier.role, tier.policy)
	}
	options.say("")
	options.say("The tier boundary is the two policies above, enforced by OpenBao.")
	options.say("A CI identity asking for a release path is refused by the store, not by Oberth.")
	options.say("What is weaker than a cluster: this server holds the signing key, so a compromised")
	options.say("server process could mint either subject. See docs/docker-engine-secrets.md.")
	return nil
}

func (options Options) ensureAuthMount(ctx context.Context, root string) error {
	var mounts map[string]json.RawMessage
	if _, err := options.call(ctx, http.MethodGet, "/v1/sys/auth", root, nil, &mounts); err != nil {
		return fmt.Errorf("localbao: read the auth mounts: %w", err)
	}
	if _, mounted := mounts[options.JWTMount+"/"]; mounted {
		return nil
	}
	if _, err := options.call(ctx, http.MethodPost, "/v1/sys/auth/"+options.JWTMount, root,
		map[string]any{"type": "jwt"}, nil); err != nil {
		return fmt.Errorf("localbao: enable the jwt auth method: %w", err)
	}
	return nil
}

func (options Options) ensureKVMount(ctx context.Context, root string) error {
	var mounts map[string]json.RawMessage
	if _, err := options.call(ctx, http.MethodGet, "/v1/sys/mounts", root, nil, &mounts); err != nil {
		return fmt.Errorf("localbao: read the secret mounts: %w", err)
	}
	if _, mounted := mounts[options.KVMount+"/"]; mounted {
		options.say("openbao: kv mount %q already exists", options.KVMount)
		return nil
	}
	if _, err := options.call(ctx, http.MethodPost, "/v1/sys/mounts/"+options.KVMount, root, map[string]any{
		"type": "kv", "options": map[string]string{"version": "2"},
	}, nil); err != nil {
		return fmt.Errorf("localbao: enable the kv v2 mount: %w", err)
	}
	options.say("openbao: kv v2 mount %q created", options.KVMount)
	return nil
}

// PutSecret writes one KV v2 secret, which is the only thing an operator does
// after the setup above.
func PutSecret(ctx context.Context, options Options, path string, fields map[string]string) error {
	options.applyDefaults()
	root, err := options.Stash.Get(ctx, RootKeychainService)
	if err != nil {
		return fmt.Errorf("localbao: the keychain holds no root token under %s: %w", RootKeychainService, err)
	}
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return errors.New("localbao: a secret path is required")
	}
	// Both the virtual oberth/upstream/... form a pipeline declares and the
	// real KV data path are accepted, because an operator reading a pipeline
	// should be able to paste what they see.
	if !strings.HasPrefix(trimmed, options.KVMount+"/data/") {
		trimmed = strings.TrimPrefix(trimmed, options.KVMount+"/")
		trimmed = options.KVMount + "/data/" + trimmed
	}
	if _, err := options.call(ctx, http.MethodPost, "/v1/"+trimmed, root,
		map[string]any{"data": fields}, nil); err != nil {
		return fmt.Errorf("localbao: write %s: %w", trimmed, err)
	}
	options.say("openbao: wrote %s", trimmed)
	return nil
}
