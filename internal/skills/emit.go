package skills

import (
	"bytes"
	"strings"
)

const (
	beginMarker = "<!-- BEGIN oberth skills -->"
	endMarker   = "<!-- END oberth skills -->"
)

func EmitSKILL(skill Skill) []byte {
	var out bytes.Buffer
	out.WriteString("---\n")
	out.WriteString("name: " + skill.Name + "\n")
	out.WriteString("description: " + skill.Description + "\n")
	out.WriteString("---\n\n")
	out.WriteString(skill.Body)
	return out.Bytes()
}

func EmitAgents(all []Skill) []byte {
	var out bytes.Buffer
	out.WriteString(beginMarker + "\n")
	for index, skill := range all {
		if index > 0 {
			out.WriteString("\n")
		}
		out.WriteString(strings.TrimRight(skill.Body, "\n") + "\n")
	}
	out.WriteString(endMarker + "\n")
	return out.Bytes()
}

func MergeMarked(existing, generated []byte) []byte {
	body := strings.ReplaceAll(string(existing), "\r\n", "\n")
	block := strings.TrimRight(string(generated), "\n")

	begin, end := markedRegion(body)
	if begin < 0 {
		if strings.TrimSpace(body) == "" {
			return []byte(block + "\n")
		}
		return []byte(strings.TrimRight(body, "\n") + "\n\n" + block + "\n")
	}
	return []byte(body[:begin] + block + body[end:])
}

func markedRegion(body string) (int, int) {
	lines := strings.SplitAfter(body, "\n")
	fenced := false
	offset := 0
	begin, end := -1, -1
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			offset += len(line)
			continue
		}
		if !fenced && trimmed == beginMarker && begin < 0 {
			begin = offset
		}
		if !fenced && trimmed == endMarker && begin >= 0 {
			end = offset + len(line)
			if strings.HasSuffix(line, "\n") {
				end--
			}
			break
		}
		offset += len(line)
	}
	if begin < 0 || end < 0 {
		return -1, -1
	}
	return begin, end
}
