package localinstall

import (
	"fmt"
	"strings"
)

// ShellProfileLine is what a developer adds to their shell profile so every
// new terminal can talk to the server. It sources the same env file the
// cluster install writes, so the two installs leave the shell in the same
// state.
func ShellProfileLine(envPath string) string {
	return "[ -f " + envPath + " ] && . " + envPath
}

// PushBanner is what the install ends with.
//
// It is the same shape the cluster install ends with, and for the same reason:
// the install is not finished when the server is running, it is finished when
// the developer knows the one command that makes their repository use it. A
// banner that lists what was configured and stops there leaves them to work
// that out from documentation.
func PushBanner(baseURL, sshHost string, sshPort int, clientKey string, repositories []string) string {
	var banner strings.Builder
	banner.WriteString("\nOberth is running.\n\n")
	banner.WriteString("  dashboard   " + baseURL + "\n")
	banner.WriteString("  git ingest  ssh://" + sshHost + ":" + fmt.Sprint(sshPort) + "\n\n")
	banner.WriteString("Point a repository at it and push:\n\n")
	banner.WriteString("    cd <your repository>\n")
	banner.WriteString("    oberth init\n")
	banner.WriteString(fmt.Sprintf("    git remote add oberth ssh://oberth@%s:%d/<name>\n", sshHost, sshPort))
	banner.WriteString("    git push oberth HEAD\n\n")
	if clientKey != "" {
		banner.WriteString("The push identity is " + clientKey + ". If it is not the key your SSH agent\n")
		banner.WriteString("offers, name it for this remote:\n\n")
		banner.WriteString("    git config --local core.sshCommand 'ssh -i " + clientKey + "'\n\n")
	}
	if len(repositories) != 0 {
		banner.WriteString("Registered repositories: " + strings.Join(repositories, ", ") + "\n\n")
	}
	banner.WriteString("Before the first push a repository needs an upstream to publish to:\n\n")
	banner.WriteString("    oberth upstream add --engine=docker --database=<data>/oberth.sqlite \\\n")
	banner.WriteString("        --upstream-key=<root>/ssh/upstream_key --known-hosts=<root>/ssh/known_hosts \\\n")
	banner.WriteString("        origin ssh://git@github.com/<org>\n\n")
	banner.WriteString("Credentialed pipelines need the local secret store once:\n\n")
	banner.WriteString("    oberth secretstore init --engine=docker\n\n")
	return banner.String()
}
