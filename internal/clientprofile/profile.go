package clientprofile

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	envFile      = "env"
	caFile       = "ca.crt"
	mcpFile      = "mcp.json"
	defaultFile  = "default"
	GitConfigKey = "oberth.profile"
)

type Profile struct {
	Name         string
	BaseURL      string
	CACert       string
	TokenCommand string
	SSHCommand   string
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("clientprofile: %q is not a profile name (letters, digits, '.', '_' and '-', starting with a letter or digit)", name)
	}
	return nil
}

func Root() (string, error) {
	if base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); base != "" {
		return filepath.Join(base, "oberth"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "oberth"), nil
}

func Dir(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "profiles", name), nil
}

func Write(name string, env string, ca []byte, mcp []byte) (string, error) {
	dir, err := Dir(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("clientprofile: create %s: %w", dir, err)
	}
	files := map[string][]byte{envFile: []byte(env), caFile: ca}
	if mcp != nil {
		files[mcpFile] = mcp
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return "", fmt.Errorf("clientprofile: write %s: %w", path, err)
		}
	}
	return dir, nil
}

func List() ([]string, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(root, "profiles"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && ValidateName(entry.Name()) == nil {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func Load(name string) (Profile, error) {
	dir, err := Dir(name)
	if err != nil {
		return Profile{}, err
	}
	profile, err := parseEnv(filepath.Join(dir, envFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Profile{}, fmt.Errorf("clientprofile: no profile named %q; `oberth profile list` names the ones that exist", name)
		}
		return Profile{}, err
	}
	profile.Name = name
	return profile, nil
}

func parseEnv(path string) (Profile, error) {
	file, err := os.Open(path) // #nosec G304 -- the client's own configuration file.
	if err != nil {
		return Profile{}, err
	}
	defer func() { _ = file.Close() }()
	var profile Profile
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "export ") {
			continue
		}
		key, raw, found := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !found {
			continue
		}
		value := raw
		if unquoted, err := strconv.Unquote(raw); err == nil {
			value = unquoted
		}
		switch key {
		case "OBERTH_BASE_URL":
			profile.BaseURL = value
		case "OBERTH_CA_CERT":
			profile.CACert = value
		case "OBERTH_TOKEN_COMMAND":
			profile.TokenCommand = value
		case "OBERTH_SSH_COMMAND":
			profile.SSHCommand = value
		}
	}
	return profile, scanner.Err()
}

func SetDefault(name string) error {
	dir, err := Dir(name)
	if err != nil {
		return err
	}
	root, err := Root()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, envFile)); err != nil {
		return fmt.Errorf("clientprofile: no profile named %q", name)
	}
	for _, file := range []string{envFile, caFile, mcpFile} {
		body, err := os.ReadFile(filepath.Join(dir, file)) // #nosec G304 -- the profile's own file.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, file), body, 0o600); err != nil {
			return fmt.Errorf("clientprofile: write %s: %w", filepath.Join(root, file), err)
		}
	}
	return os.WriteFile(filepath.Join(root, defaultFile), []byte(name+"\n"), 0o600)
}

func Default() string {
	root, err := Root()
	if err != nil {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(root, defaultFile)) // #nosec G304 -- the client's own file.
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func ForCheckout(dir string) string {
	out, err := exec.Command("git", "-C", dir, "config", "--local", "--get", GitConfigKey).Output() // #nosec G204 -- fixed verbs.
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func Pin(dir, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	return exec.Command("git", "-C", dir, "config", "--local", GitConfigKey, name).Run() // #nosec G204 -- fixed verbs.
}
