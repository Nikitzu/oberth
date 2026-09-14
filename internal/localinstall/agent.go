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
func RenderLaunchdAgent(binary string, arguments []string, layout Layout, path string) ([]byte, error) {
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
	// launchd starts an agent with a minimal PATH that does not include
	// /usr/local/bin, which is where the docker CLI and most of Homebrew live.
	// Carrying the PATH the install ran under is what makes the agent see the
	// same tools the operator does; without it the server starts, answers, and
	// refuses every push with "docker is not on PATH".
	environment := ""
	if strings.TrimSpace(path) != "" {
		escapedPath, pathErr := escapeXML(path)
		if pathErr != nil {
			return nil, pathErr
		}
		environment = "\t<key>EnvironmentVariables</key>\n\t<dict>\n" +
			"\t\t<key>PATH</key>\n\t\t<string>" + escapedPath + "</string>\n\t</dict>\n"
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + label + `</string>
` + program.String() + environment + `	<key>RunAtLoad</key>
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

const SystemdUnit = "oberth.service"

func RenderSystemdUnit(binary string, arguments []string, layout Layout, path string) []byte {
	quoted := make([]string, 0, len(arguments)+1)
	quoted = append(quoted, systemdQuote(binary))
	for _, argument := range arguments {
		quoted = append(quoted, systemdQuote(argument))
	}
	var out strings.Builder
	out.WriteString("[Unit]\n")
	out.WriteString("Description=Oberth CI server (docker engine)\n")
	out.WriteString("After=network-online.target docker.service\n\n")
	out.WriteString("[Service]\n")
	out.WriteString("ExecStart=" + strings.Join(quoted, " ") + "\n")
	out.WriteString("WorkingDirectory=" + layout.Root + "\n")
	out.WriteString("Environment=PATH=" + path + "\n")
	out.WriteString("StandardOutput=append:" + layout.Logs + "\n")
	out.WriteString("StandardError=append:" + layout.Logs + "\n")
	out.WriteString("Restart=always\nRestartSec=5\n\n")
	out.WriteString("[Install]\nWantedBy=default.target\n")
	return []byte(out.String())
}

func systemdQuote(value string) string {
	if !strings.ContainsAny(value, " \t\"'\\") {
		return value
	}
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "\"", "\\\"") + "\""
}
