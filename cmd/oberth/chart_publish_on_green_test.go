package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const publishOnGreenTestDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

// --publish-on-green is declared with BoolVar, so an empty value is not a
// default, it is a parse error the server dies on. `helm upgrade
// --reuse-values` reuses the previous release's values and never merges a
// newer chart's defaults, so a release created before publishOnGreen existed
// carries no such key and used to render "--publish-on-green=" into that flag.
//
// The explicit-false case is the one that matters most, and it is why the
// template does not use sprig's `default`: `default` treats false as empty, so
// an operator who asked for false would have been given the fallback instead.
func TestChartRendersPublishOnGreenAsAParseableBool(t *testing.T) {
	for _, test := range []struct {
		name   string
		values string
		want   string
	}{
		{"values predate the key", "publishOnGreen: null\n", "--publish-on-green=false"},
		{"explicitly off", "publishOnGreen: false\n", "--publish-on-green=false"},
		{"explicitly on", "publishOnGreen: true\n", "--publish-on-green=true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := filepath.Join(t.TempDir(), "values.yaml")
			if err := os.WriteFile(values, []byte(test.values), 0o600); err != nil {
				t.Fatalf("write values: %v", err)
			}
			rendered, err := exec.Command("helm", "template", "oberth", "../../charts/oberth",
				"--set", "image.ref=example.invalid/oberth@"+publishOnGreenTestDigest,
				"-f", values,
			).CombinedOutput()
			if err != nil {
				t.Fatalf("render chart: %v\n%s", err, rendered)
			}
			if !strings.Contains(string(rendered), test.want) {
				t.Errorf("rendered chart is missing %q", test.want)
			}
		})
	}
}
