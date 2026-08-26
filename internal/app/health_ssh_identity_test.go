package app

import (
	"testing"

	"github.com/oberthci/oberth/internal/model"
)

// Readiness once demanded an SSH identity for every upstream that was not
// local, so a deployment whose upstream was https and deliberately keyless
// never became ready: the probe asked for a credential the design had removed.
func TestOnlySSHUpstreamsNeedAnIdentity(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		upstreams []model.Upstream
		want      bool
	}{
		"https only":   {[]model.Upstream{{Kind: "https"}}, false},
		"local only":   {[]model.Upstream{{Kind: "local"}}, false},
		"https, local": {[]model.Upstream{{Kind: "https"}, {Kind: "local"}}, false},
		"ssh only":     {[]model.Upstream{{Kind: "ssh"}}, true},
		"ssh among others": {
			[]model.Upstream{{Kind: "https"}, {Kind: "ssh"}, {Kind: "local"}}, true,
		},
		"none": {nil, false},
	} {
		if got := requiresSSHIdentity(testCase.upstreams); got != testCase.want {
			t.Errorf("%s: requiresSSHIdentity = %v, want %v", name, got, testCase.want)
		}
	}
}
