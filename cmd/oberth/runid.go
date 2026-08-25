package main

import (
	"fmt"
	"strings"
)

// runIDLength is how long a run identifier actually is. The push banner and the
// dashboard both abbreviate it, so the most common reason a lookup finds
// nothing is that the abbreviation was pasted rather than the identifier.
const runIDLength = 32

// noArtifactsMessage explains an empty artifact listing without asserting more
// than is known.
//
// A run that kept nothing and a run that never existed are indistinguishable
// here: the store writes no directory until something is collected, so both
// look identical on disk. Saying "kept no artifacts" is therefore a guess
// whenever the identifier is not a whole one, and it is exactly the wrong guess
// when someone has pasted the twelve characters the push banner prints.
func noArtifactsMessage(runID string) string {
	if len(runID) != runIDLength && looksAbbreviated(runID) {
		return fmt.Sprintf(
			"no artifacts for %q, which is %d characters; a run ID is %d. "+
				"The push banner and the dashboard abbreviate it, so this may be a prefix rather than the whole thing\n",
			runID, len(runID), runIDLength)
	}
	return fmt.Sprintf("run %s kept no artifacts\n", runID)
}

// looksAbbreviated reports whether the value could be the front of a run ID,
// rather than an identifier of some other shape entirely. A deployment whose
// run IDs are not 32 hex characters should still get the plain message.
func looksAbbreviated(value string) bool {
	if value == "" || len(value) >= runIDLength {
		return false
	}
	for _, character := range value {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}

// resolveRunIDPrefix turns the abbreviation a reader has to hand into the
// identifier the API wants.
//
// The push banner prints twelve characters and the dashboard abbreviates too,
// so a prefix is the common case rather than the exceptional one. Refusing it
// sends the reader to list runs and copy the long form, which the server can
// answer instead.
//
// A whole identifier is returned unchanged, so the common path costs nothing.
// An ambiguous prefix names its candidates rather than picking one, because
// guessing between two runs is worse than asking.
func resolveRunIDPrefix(given string, known []string) (string, error) {
	if !looksAbbreviated(given) {
		return given, nil
	}
	var matches []string
	for _, candidate := range known {
		if strings.HasPrefix(candidate, given) {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no run starts with %q; it is %d characters and a run ID is %d",
			given, len(given), runIDLength)
	default:
		return "", fmt.Errorf("%q matches %d runs: %s", given, len(matches), strings.Join(matches, ", "))
	}
}
