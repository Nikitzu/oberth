package installer

import (
	"strings"
	"testing"
)

// A deployment reached on an address its certificate does not carry fails TLS
// with a hostname mismatch, and no trust anchor repairs that. The installer is
// the only component that knows both which addresses this install will be
// reached on and that the certificate is about to be generated, so it is the
// place that names them.
func TestOberthHelmArgsNamesTheAddressesTheCertificateMustCarry(t *testing.T) {
	t.Parallel()

	args := strings.Join(OberthHelmArgs(Config{
		TLSExtraDNSNames: []string{"localhost", "oberth.example.internal"},
		TLSExtraIPs:      []string{"127.0.0.1"},
	}, OpenBaoResult{}, RekorResult{}), " ")

	for _, want := range []string{
		"tls.extraDNSNames[0]=localhost",
		"tls.extraDNSNames[1]=oberth.example.internal",
		"tls.extraIPs[0]=127.0.0.1",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("helm args do not set %q; got %s", want, args)
		}
	}
}

// An install that names no extra addresses must send no extra values, so the
// chart default stays the four in-cluster names.
func TestOberthHelmArgsSendsNoCertificateNamesWhenNoneWereGiven(t *testing.T) {
	t.Parallel()

	args := strings.Join(OberthHelmArgs(Config{}, OpenBaoResult{}, RekorResult{}), " ")
	if strings.Contains(args, "tls.extra") {
		t.Errorf("helm args widen the certificate for an install that named nothing: %s", args)
	}
}

// A kind install is reached over the chart's fixed NodePorts on the loopback
// interface, so localhost and 127.0.0.1 are always true of it. Adding them
// automatically is what removes the /etc/hosts step a host-side client would
// otherwise need.
func TestKindInstallNamesLoopbackOnTheCertificate(t *testing.T) {
	t.Parallel()

	dnsNames, ips := certificateNamesForKind(nil, nil)
	if !contains(dnsNames, "localhost") {
		t.Errorf("a kind install does not name localhost: %v", dnsNames)
	}
	if !contains(ips, "127.0.0.1") {
		t.Errorf("a kind install does not name 127.0.0.1: %v", ips)
	}
}

// An operator who names their own addresses keeps them, and does not lose the
// loopback names that are true regardless.
func TestKindInstallKeepsOperatorNamesAndDoesNotDuplicateLoopback(t *testing.T) {
	t.Parallel()

	dnsNames, ips := certificateNamesForKind([]string{"oberth.example.internal", "localhost"}, []string{"127.0.0.1"})

	if !contains(dnsNames, "oberth.example.internal") {
		t.Errorf("operator-named address was dropped: %v", dnsNames)
	}
	if countOf(dnsNames, "localhost") != 1 {
		t.Errorf("localhost appears %d times, want exactly 1: %v", countOf(dnsNames, "localhost"), dnsNames)
	}
	if countOf(ips, "127.0.0.1") != 1 {
		t.Errorf("127.0.0.1 appears %d times, want exactly 1: %v", countOf(ips, "127.0.0.1"), ips)
	}
}

func contains(values []string, want string) bool { return countOf(values, want) > 0 }

func countOf(values []string, want string) int {
	total := 0
	for _, value := range values {
		if value == want {
			total++
		}
	}
	return total
}
