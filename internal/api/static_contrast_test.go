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
	// theme.css is the fork seam: it is empty upstream and a deployment
	// replaces it to retheme without editing app.css. A fork that redefines
	// these tokens has to clear AA too, and a brand colour chosen for a logo
	// is exactly the kind that does not. Reading whichever file actually
	// defines them means this test follows the tokens rather than the file.
	css := string(staticAssets["app.css"].body)
	if theme := string(staticAssets["theme.css"].body); strings.Contains(theme, "--pass-bg") {
		css = theme
	}
	light := themeBlock(t, css, "light{")
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

// token resolves a custom property to a literal colour, following one level of
// var() indirection. A fork names its brand palette once and refers to it, so
// --pass:var(--tz-green-ink) is the normal shape and a test that only reads
// hex would report the tokens missing rather than checking them.
func token(t *testing.T, block, name string) string {
	t.Helper()
	value := rawToken(t, block, name, true)
	if strings.HasPrefix(value, "#") {
		return value
	}
	reference := regexp.MustCompile(`var\((--[a-zA-Z0-9-]+)\)`).FindStringSubmatch(value)
	if reference == nil {
		t.Fatalf("token %s is %q, neither a colour nor a var() reference", name, value)
	}
	// Palette entries live outside the theme block, so search the whole sheet.
	resolved := rawToken(t, string(staticAssets["app.css"].body)+
		string(staticAssets["theme.css"].body), reference[1], true)
	if !strings.HasPrefix(resolved, "#") {
		t.Fatalf("token %s resolves to %q, which is not a colour", name, resolved)
	}
	return resolved
}

func rawToken(t *testing.T, source, name string, required bool) string {
	t.Helper()
	match := regexp.MustCompile(regexp.QuoteMeta(name) + `: *([^;}]+)`).FindStringSubmatch(source)
	if match == nil {
		if required {
			t.Fatalf("token %s not found", name)
		}
		return ""
	}
	return strings.TrimSpace(match[1])
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

// TestIssuePaginationCannotDoubleLoad guards the append path.
//
// The observer fires again while a request is still in flight, so without a
// re-entry guard the same cursor is requested twice and every row in that page
// appears twice. Nothing in the UI would look broken; the list would just
// quietly repeat itself.
func TestIssuePaginationCannotDoubleLoad(t *testing.T) {
	script := string(staticAssets["app.js"].body)
	body := regexp.MustCompile(`async function loadMoreIssues\(\)[\s\S]*?\n}`).FindString(script)
	if body == "" {
		t.Fatal("loadMoreIssues not found in app.js")
	}
	// Assert the flag gates the early return, not merely that it is mentioned.
	// A first draft of this test checked only that "state.issueLoading" appeared
	// somewhere in the function, which stayed true when the guard was deleted
	// from the condition and left as an assignment.
	guard := regexp.MustCompile(`if \([^)]*state\.issueLoading[^)]*\)\s*return`)
	if !guard.MatchString(body) {
		t.Fatal("state.issueLoading does not gate an early return, so a second scroll event duplicates a page")
	}
	if !strings.Contains(body, "finally") {
		t.Fatal("the in-flight guard is not released in a finally, so one failed request wedges the list forever")
	}
}

// TestTheIssueListKeepsAKeyboardPath is why this is append-on-scroll rather
// than infinite scroll. An observer is a convenience for a pointer; the button
// is what a keyboard or screen reader reaches, and removing it would strand
// them at the first page.
func TestTheIssueListKeepsAKeyboardPath(t *testing.T) {
	script := string(staticAssets["app.js"].body)
	if !strings.Contains(script, `data-action="issue-more"`) {
		t.Fatal("no load-more button; scrolling would be the only way to reach later pages")
	}
	if !strings.Contains(script, "IntersectionObserver") {
		t.Fatal("no observer; the button is the only path and scrolling does nothing")
	}
}
