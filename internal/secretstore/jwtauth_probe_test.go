package secretstore

import (
	"context"
	"os"
	"testing"
)

// This file is a probe, not a feature. It exists to answer one question the
// docker-engine spike needed answered before recommending a local secrets
// design: does this client, whose login is described throughout as
// "Kubernetes auth", work against an OpenBao *jwt* auth mount with no code
// change at all?
//
// It does. Both methods post {"jwt": ..., "role": ...} to auth/<mount>/login,
// and New() validates AuthMountPath only as a clean path segment rather than
// comparing it to "kubernetes", so the mount is genuinely parameterised. The
// only Kubernetes-specific thing left is where the JWT comes from: a file at
// ServiceAccountTokenPath, which the kubelet happens to project in-cluster and
// which anything can write out of cluster.
//
// The consequence matters for the security story. A local profile does NOT
// have to move tier separation out of OpenBao and into Oberth's own process:
// a jwt role can bind a subject claim exactly as a kubernetes role binds a
// ServiceAccount name, and OpenBao still refuses the CI identity a release
// credential. TestJWTCITierIsRefusedTheReleasePath is that claim under test.
//
// Skipped unless OBERTH_JWT_PROBE_ADDR names a live OpenBao configured as the
// spike report's "one-time setup" section describes.
func probeConfig(t *testing.T, role, tokenPathEnv string) Config {
	t.Helper()
	address := os.Getenv("OBERTH_JWT_PROBE_ADDR")
	if address == "" {
		t.Skip("set OBERTH_JWT_PROBE_ADDR (and the token path variables) to probe a live OpenBao")
	}
	tokenPath := os.Getenv(tokenPathEnv)
	if tokenPath == "" {
		t.Skipf("set %s to a file holding a signed JWT for role %s", tokenPathEnv, role)
	}
	return Config{
		Address: address, AllowInsecureHTTP: true,
		AuthMountPath: "jwt", Role: role, ServiceAccountTokenPath: tokenPath,
	}
}

func TestJWTAuthMountNeedsNoClientChange(t *testing.T) {
	for _, probe := range []struct{ name, role, tokenEnv, path, field string }{
		{"ci tier", "oberth-ci", "OBERTH_JWT_PROBE_CI_TOKEN",
			"oberth/data/upstream/acme/widget/deploy", "token"},
		{"release tier", "oberth-release", "OBERTH_JWT_PROBE_RELEASE_TOKEN",
			"oberth/data/release/signing", "key"},
	} {
		t.Run(probe.name, func(t *testing.T) {
			client, err := New(probeConfig(t, probe.role, probe.tokenEnv))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := client.VerifyLogin(context.Background()); err != nil {
				t.Fatalf("VerifyLogin against a jwt mount: %v", err)
			}
			values, err := client.FetchKV(context.Background(), []string{probe.path})
			if err != nil {
				t.Fatalf("FetchKV: %v", err)
			}
			if len(values[probe.path][probe.field]) == 0 {
				t.Fatalf("no value at %s key %q", probe.path, probe.field)
			}
		})
	}
}

// The property the investigation report expected to lose. It survives: the
// refusal comes from OpenBao's policy engine, not from Oberth declining to
// ask.
func TestJWTCITierIsRefusedTheReleasePath(t *testing.T) {
	client, err := New(probeConfig(t, "oberth-ci", "OBERTH_JWT_PROBE_CI_TOKEN"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.FetchKV(context.Background(), []string{"oberth/data/release/signing"}); err == nil {
		t.Fatal("the CI identity read a release path; tier separation is not enforced by the store")
	}
}
