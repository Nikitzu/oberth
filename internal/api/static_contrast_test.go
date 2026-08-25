package api

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The dashboard puts status text on a tint of its own colour. That pattern is
// one careless hex away from failing WCAG AA: the light theme once measured
// 4.27:1 on --run against a 4.5 requirement, and a fork's brand tokens measured
// 2.37:1, both of which looked fine to the eye that chose them.
//
// These tests compute the ratios from the stylesheet the binary actually
// serves, so a token edit that breaks contrast fails the build rather than
// shipping.

const (
	textContrast = 4.5 // AA, body text
	markContrast = 3.0 // AA, non-text such as the status dot
)

func TestLightThemeStatusInkClearsAA(t *testing.T) {
	css := string(staticAssets["app.css"].body)
	light := themeBlock(t, css, "body.light{")
	const white = "#ffffff"

	for _, status := range []string{"pass", "fail", "run"} {
		t.Run(status, func(t *testing.T) {
			ink := token(t, light, "--"+status)
			mark := token(t, light, "--"+status+"-mark")
			// The tint is the status colour at 0x14/255 over the panel.
			tint := token(t, light, "--"+status+"-bg")
			background := composite(t, tint, white)

			if got := contrast(t, ink, background); got < textContrast {
				t.Fatalf("--%s text %s on %s is %.2f:1, below AA %.1f",
					status, ink, background, got, textContrast)
			}
			if got := contrast(t, mark, background); got < markContrast {
				t.Fatalf("--%s-mark %s on %s is %.2f:1, below AA %.1f for non-text",
					status, mark, background, got, markContrast)
			}
		})
	}
}

// TestTheIssueGridHasNoContentSizedTracks guards the alignment fix.
//
// Every row is its own grid container, so an auto or max-content track resolves
// against that row alone. A "closed" chip and an "open" chip then produce
// different column widths, the header lines up with nothing, and the repo name
// drifts row to row. Only fixed and fr tracks resolve identically across
// separate containers.
func TestTheIssueGridHasNoContentSizedTracks(t *testing.T) {
	css := string(staticAssets["app.css"].body)
	rule := regexp.MustCompile(`\.issue-cols\{[^}]*grid-template-columns:([^;]+);`).FindStringSubmatch(css)
	if rule == nil {
		t.Fatal("no .issue-cols grid-template-columns in app.css")
	}
	for _, track := range strings.Fields(rule[1]) {
		if track == "auto" || strings.Contains(track, "max-content") || strings.Contains(track, "min-content") {
			t.Fatalf("track %q is content-sized, so it resolves per row and the columns cannot align", track)
		}
	}
}

// TestTheIssueRowFillsEveryTrack keeps the child count and the track count in
// step. The original row declared six tracks for eight children, so the last
// two wrapped onto an implicit second row and every row was double height.
func TestTheIssueRowFillsEveryTrack(t *testing.T) {
	css := string(staticAssets["app.css"].body)
	script := string(staticAssets["app.js"].body)

	rule := regexp.MustCompile(`\.issue-cols\{[^}]*grid-template-columns:([^;]+);`).FindStringSubmatch(css)
	if rule == nil {
		t.Fatal("no .issue-cols grid-template-columns in app.css")
	}
	tracks := len(strings.Fields(rule[1]))

	row := regexp.MustCompile(`class="issue-cols issue-row"[\s\S]*?</button>`).FindString(script)
	if row == "" {
		t.Fatal("no issue row template in app.js")
	}
	children := strings.Count(row, "<span")
	if pillCalls := strings.Count(row, "${pill("); pillCalls > 0 {
		children += pillCalls // pill() renders one span of its own
	}
	if children != tracks {
		t.Fatalf("issue row renders %d children into %d tracks; the surplus wraps to a second row",
			children, tracks)
	}
}

func themeBlock(t *testing.T, css, marker string) string {
	t.Helper()
	start := strings.Index(css, marker)
	if start < 0 {
		t.Fatalf("no %q block in app.css", marker)
	}
	end := strings.Index(css[start:], "}")
	if end < 0 {
		t.Fatalf("unterminated %q block", marker)
	}
	return css[start : start+end]
}

func token(t *testing.T, block, name string) string {
	t.Helper()
	match := regexp.MustCompile(regexp.QuoteMeta(name) + `: *(#[0-9a-fA-F]{6,8})`).FindStringSubmatch(block)
	if match == nil {
		t.Fatalf("token %s not found", name)
	}
	return match[1]
}

// composite flattens an eight-digit hex (colour plus alpha) onto an opaque
// background, which is what the browser does before contrast applies.
func composite(t *testing.T, layer, background string) string {
	t.Helper()
	if len(layer) != 9 {
		return layer
	}
	alpha := float64(component(t, layer[7:9])) / 255
	out := "#"
	for i := 0; i < 3; i++ {
		f := float64(component(t, layer[1+i*2:3+i*2]))
		b := float64(component(t, background[1+i*2:3+i*2]))
		out += strconv.FormatInt(int64(math.Round(f*alpha+b*(1-alpha))), 16)
	}
	return normalise(out)
}

func normalise(value string) string {
	out := "#"
	for _, part := range strings.Split(value[1:], "") {
		out += part
	}
	return out
}

func component(t *testing.T, pair string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(pair, 16, 32)
	if err != nil {
		t.Fatalf("bad hex %q: %v", pair, err)
	}
	return value
}

func contrast(t *testing.T, foreground, background string) float64 {
	t.Helper()
	a, b := luminance(t, foreground), luminance(t, background)
	high, low := math.Max(a, b), math.Min(a, b)
	return (high + 0.05) / (low + 0.05)
}

func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	channels := [3]float64{}
	for i := 0; i < 3; i++ {
		value := float64(component(t, hex[1+i*2:3+i*2])) / 255
		if value <= 0.04045 {
			channels[i] = value / 12.92
		} else {
			channels[i] = math.Pow((value+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}
