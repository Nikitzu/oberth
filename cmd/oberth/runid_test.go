package main

import (
	"strings"
	"testing"
)

// The twelve-character form is what the push banner prints, so it is the value
// someone is most likely to paste. Reporting "kept no artifacts" for it states
// something the command cannot know and sends the reader looking for a
// collection failure that never happened.
func TestAnAbbreviatedRunIDIsNamedAsSuch(t *testing.T) {
	message := noArtifactsMessage("b9f31bccd797")
	if strings.Contains(message, "kept no artifacts") {
		t.Fatalf("an abbreviated ID was reported as a run that kept nothing: %q", message)
	}
	if !strings.Contains(message, "32") || !strings.Contains(message, "12") {
		t.Fatalf("message %q does not name both lengths", message)
	}
}

func TestAWholeRunIDReportsPlainly(t *testing.T) {
	message := noArtifactsMessage("b9f31bccd7974fe36980cfb6f72f81e2")
	if !strings.Contains(message, "kept no artifacts") {
		t.Fatalf("a whole ID did not get the plain message: %q", message)
	}
}

// A deployment whose run IDs are not 32 hex characters must still get the plain
// message rather than a lecture about a format it does not use.
func TestAnIdentifierOfAnotherShapeReportsPlainly(t *testing.T) {
	for _, id := range []string{"run-slow-02", "RUN123", "not-hex-at-all", ""} {
		t.Run(id, func(t *testing.T) {
			if message := noArtifactsMessage(id); !strings.Contains(message, "kept no artifacts") {
				t.Fatalf("id %q got the abbreviation hint: %q", id, message)
			}
		})
	}
}
