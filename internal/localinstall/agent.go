package localinstall

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// LaunchdLabel is the agent identifier. One per user, so a second install
// replaces the first rather than starting a second server on the same ports.
const LaunchdLabel = "ci.oberth.server"

// RenderLaunchdAgent produces the plist that keeps the server running across
// logins and reboots.
//
// KeepAlive rather than RunAtLoad alone: the point of the agent is that the
// developer never thinks about the server, and a server that stopped because
// Docker was not ready yet at login would be exactly the thing they have to
// think about. The log paths are inside the install root so a failed start
// leaves its reason somewhere findable rather than in the system log.
func RenderLaunchdAgent(binary string, arguments []string, layout Layout) ([]byte, error) {
	var program strings.Builder
	program.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, argument := range append([]string{binary}, arguments...) {
		escaped, err := escapeXML(argument)
		if err != nil {
			return nil, err
		}
		program.WriteString("\t\t<string>" + escaped + "</string>\n")
	}
	program.WriteString("\t</array>\n")

	label, err := escapeXML(LaunchdLabel)
	if err != nil {
		return nil, err
	}
	logPath, err := escapeXML(layout.Logs)
	if err != nil {
		return nil, err
	}
	root, err := escapeXML(layout.Root)
	if err != nil {
		return nil, err
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + label + `</string>
` + program.String() + `	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>WorkingDirectory</key>
	<string>` + root + `</string>
	<key>StandardOutPath</key>
	<string>` + logPath + `</string>
	<key>StandardErrorPath</key>
	<string>` + logPath + `</string>
</dict>
</plist>
`), nil
}

// escapeXML refuses anything a plist cannot carry rather than mangling it. A
// path with a newline in it is a mistake worth reporting, not worth encoding.
func escapeXML(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("localinstall: %q cannot appear in a launchd agent", value)
	}
	var escaped strings.Builder
	if err := xml.EscapeText(&escaped, []byte(value)); err != nil {
		return "", err
	}
	return escaped.String(), nil
}
