package installer

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// A bare host/org is HTTPS, and HTTPS authenticates with a token belonging to
// the person installing. Nobody should be asked to arrange a deploy key, which
// on an organisation repository needs admin rights and grants this server
// standing write access afterwards.
func TestBareOrgAsksForATokenAndNotADeployKey(t *testing.T) {
	t.Parallel()
	host := &fakeOberthHost{
		t:            t,
		uplinkOutput: "Uplink token for tester@box (shown once):\ntok_123\n",
	}
	keyPath, _ := stageUplinkKeyPair(t)

	// github.com/acme, then the token, the uplink key path, the identity, and
	// a declined SSH config.
	input := strings.NewReader("github.com/acme\nghp_from_operator\n" + keyPath + "\ntester@box\nn\n")
	var buf bytes.Buffer
	deps := onboardingDeps(t, host, &buf, input, true)
	cfg := Config{}
	_ = cfg.Validate()

	if err := finishInstallTest(context.Background(), cfg, deps, &buf); err != nil {
		t.Fatal(err)
	}
	output := buf.String()

	if strings.Contains(output, "[G]enerate / [P]rovide: ") {
		t.Fatalf("an HTTPS upstream asked for a deploy key:\n%s", output)
	}
	if !strings.Contains(output, "personal access token: ") {
		t.Fatalf("no token was asked for:\n%s", output)
	}
	if strings.Contains(output, "ghp_from_operator") {
		t.Fatalf("the token was echoed back to the terminal:\n%s", output)
	}

	var joined string
	for _, argv := range host.addArgv {
		joined = strings.Join(argv, " ")
	}
	if !strings.Contains(joined, "https://github.com/acme") {
		t.Fatalf("upstream was not registered over https: %s", joined)
	}
}
