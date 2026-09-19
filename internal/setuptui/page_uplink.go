package setuptui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type sshKey struct {
	path        string
	algorithm   string
	fingerprint string
}

type uplinkPage struct {
	identity   string
	keys       []sshKey
	keyCursor  int
	focusField int // 0=identity, 1=key list
	errMsg     string
}

func newUplinkPage() *uplinkPage {
	return &uplinkPage{}
}

func (p *uplinkPage) title() string    { return "crew manifest" }
func (p *uplinkPage) question() string { return "Who are you?" }

func (p *uplinkPage) init(state *WizardState) tea.Cmd {
	if state.UplinkIdentity != "" {
		p.identity = state.UplinkIdentity
	} else {
		// Default identity from hostname.
		user := os.Getenv("USER")
		if user == "" {
			user = "admin"
		}
		host, _ := os.Hostname()
		if host == "" {
			host = "localhost"
		}
		p.identity = user + "@" + host
	}

	// Scan ~/.ssh for public keys (S9: private keys are never read).
	p.keys = scanSSHPublicKeys()
	p.keyCursor = 0
	p.focusField = 0
	p.errMsg = ""

	// Pre-select if state already has a key path.
	if state.SSHKeyPath != "" {
		for i, k := range p.keys {
			if k.path == state.SSHKeyPath {
				p.keyCursor = i
				break
			}
		}
	}

	return nil
}

func (p *uplinkPage) update(msg tea.Msg, state *WizardState) (page, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab":
			p.focusField = (p.focusField + 1) % 2
		case "shift+tab":
			p.focusField = (p.focusField + 1) % 2
		case "up", "k":
			if p.focusField == 1 && p.keyCursor > 0 {
				p.keyCursor--
			}
		case "down", "j":
			if p.focusField == 1 && p.keyCursor < len(p.keys)-1 {
				p.keyCursor++
			}
		case "enter":
			if p.identity == "" {
				p.errMsg = "identity is required"
				return p, nil
			}
			if len(p.keys) == 0 {
				p.errMsg = "no SSH public keys found in ~/.ssh"
				return p, nil
			}
			state.UplinkIdentity = p.identity
			state.SSHKeyPath = p.keys[p.keyCursor].path
			return p, func() tea.Msg { return pageCompleteMsg{} }
		case "escape":
			return p, func() tea.Msg { return pageBackMsg{} }
		case "backspace":
			if p.focusField == 0 && len(p.identity) > 0 {
				p.identity = p.identity[:len(p.identity)-1]
			}
		default:
			text := msg.String()
			if p.focusField == 0 && len(text) == 1 {
				p.identity += text
			}
		}
	}
	return p, nil
}

func (p *uplinkPage) view(_ *WizardState, _, _ int) string {
	var b strings.Builder

	b.WriteString("  " + sQuestion.Render(p.question()) + "\n\n")

	// Identity field.
	idCursor := "  "
	idLabelStyle := sMuted
	if p.focusField == 0 {
		idCursor = lipgloss.NewStyle().Foreground(cPurple).Render("❯ ")
		idLabelStyle = lipgloss.NewStyle().Foreground(cPurple)
	}
	idInput := lipgloss.NewStyle().
		Background(cLine).
		Foreground(cFg).
		Padding(0, 1).
		Render(p.identity)
	_, _ = fmt.Fprintf(&b, "  %s%-12s %s", idCursor, idLabelStyle.Render("identity"), idInput)
	b.WriteString("                      " + sMuted.Render("<identity>@<host>") + "\n\n")

	// SSH public key list.
	b.WriteString("  " + sMuted.Render("ssh public key") + "   " +
		sMuted.Render("(~/.ssh — ") + sGo.Render("private keys are never read") + sMuted.Render(")") + "\n")

	if len(p.keys) == 0 {
		b.WriteString("  " + sFail.Render("  no public keys found in ~/.ssh") + "\n")
	} else {
		var listContent strings.Builder
		for i, k := range p.keys {
			cursor := "  "
			if i == p.keyCursor {
				cursor = lipgloss.NewStyle().Foreground(cPurple).Render("❯ ")
			}
			name := filepath.Base(k.path)
			algo := sInfo.Render(k.algorithm)
			fp := sText.Render(k.fingerprint)
			_, _ = fmt.Fprintf(&listContent, " %s %-20s %s    %s\n", cursor, name, algo, fp)
		}

		box := sListBox.Render(listContent.String())
		b.WriteString("  " + box + "\n")
	}

	// Command preview.
	if len(p.keys) > 0 && p.keyCursor < len(p.keys) {
		keyPath := p.keys[p.keyCursor].path
		cmd := fmt.Sprintf("oberth uplink add - %s < %s", p.identity, keyPath)
		b.WriteString("\n  " + sMuted.Render("will run: ") + sInfo.Render(cmd) + "\n")
	}

	if p.errMsg != "" {
		b.WriteString("\n  " + sFail.Render(p.errMsg) + "\n")
	}

	return b.String()
}

// scanSSHPublicKeys looks for *.pub files in ~/.ssh.
// Only public key files are read (S9).
func scanSSHPublicKeys() []sshKey {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	sshDir := filepath.Join(home, ".ssh")

	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return nil
	}

	var keys []sshKey
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pub") {
			continue
		}
		path := filepath.Join(sshDir, entry.Name())
		data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed from ~/.ssh directory listing, not user input
		if err != nil {
			continue
		}

		parts := strings.Fields(string(data))
		algo := "unknown"
		fp := ""
		if len(parts) >= 1 {
			algo = parts[0]
			// Shorten common prefixes.
			algo = strings.TrimPrefix(algo, "ssh-")
		}
		if len(parts) >= 2 {
			// Generate a short fingerprint display from the key data.
			fpFull := parts[1]
			if len(fpFull) > 12 {
				fp = "SHA256:" + fpFull[:8] + "..." + fpFull[len(fpFull)-4:]
			} else {
				fp = fpFull
			}
		}

		keys = append(keys, sshKey{
			path:        path,
			algorithm:   algo,
			fingerprint: fp,
		})
	}

	return keys
}
