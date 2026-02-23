package config

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

const (
	// CodexDefaultOriginator matches Codex CLI default originator naming.
	CodexDefaultOriginator = "codex_cli_rs"
	// CodexClientVersion is sent to upstream to match Codex CLI behavior.
	CodexClientVersion    = "0.104.0"
	originatorOverrideEnv = "CODEX_INTERNAL_ORIGINATOR_OVERRIDE"
)

var (
	cachedOSVersionOnce sync.Once
	cachedOSVersion     string
)

// CodexOriginator returns the originator used by Codex CLI.
func CodexOriginator() string {
	if candidate := strings.TrimSpace(os.Getenv(originatorOverrideEnv)); isValidHeaderValue(candidate) {
		return candidate
	}
	return CodexDefaultOriginator
}

// ApplyCodexDefaultHeaders applies the default Codex CLI request headers:
// User-Agent, originator, version, and optional OpenAI org/project headers.
func ApplyCodexDefaultHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Set("User-Agent", CodexUserAgent())
	headers.Set("originator", CodexOriginator())
	headers.Set("version", CodexClientVersion)

	if org := strings.TrimSpace(os.Getenv("OPENAI_ORGANIZATION")); org != "" {
		headers.Set("OpenAI-Organization", org)
	}
	if project := strings.TrimSpace(os.Getenv("OPENAI_PROJECT")); project != "" {
		headers.Set("OpenAI-Project", project)
	}
}

// CodexUserAgent builds a Codex CLI-style User-Agent:
// <originator>/<version> (<os_type> <os_version>; <arch>) <terminal_token>
func CodexUserAgent() string {
	prefix := fmt.Sprintf("%s/%s (%s %s; %s) %s",
		CodexOriginator(),
		CodexClientVersion,
		codexOSType(),
		codexOSVersion(),
		codexArch(),
		codexTerminalUserAgent(),
	)
	return sanitizeUserAgent(prefix, prefix)
}

func codexOSType() string {
	switch runtime.GOOS {
	case "darwin":
		return "Mac OS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	case "freebsd":
		return "FreeBSD"
	case "openbsd":
		return "OpenBSD"
	case "netbsd":
		return "NetBSD"
	default:
		return runtime.GOOS
	}
}

func codexArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "arm64"
	default:
		return runtime.GOARCH
	}
}

func codexOSVersion() string {
	cachedOSVersionOnce.Do(func() {
		cachedOSVersion = detectOSVersion()
		if cachedOSVersion == "" {
			cachedOSVersion = "unknown"
		}
	})
	return cachedOSVersion
}

func detectOSVersion() string {
	switch runtime.GOOS {
	case "darwin":
		if v := commandOutput("sw_vers", "-productVersion"); v != "" {
			return v
		}
		if v := commandOutput("uname", "-r"); v != "" {
			return v
		}
	case "linux":
		return detectLinuxVersion()
	}
	return ""
}

func commandOutput(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func detectLinuxVersion() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	values := map[string]string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if parsed, err := strconv.Unquote(v); err == nil {
			v = parsed
		} else {
			v = strings.Trim(v, "\"")
		}
		values[k] = v
	}
	for _, key := range []string{"VERSION_ID", "VERSION"} {
		if v := strings.TrimSpace(values[key]); v != "" {
			return v
		}
	}
	return ""
}

func codexTerminalUserAgent() string {
	if termProgram := strings.TrimSpace(os.Getenv("TERM_PROGRAM")); termProgram != "" {
		if version := strings.TrimSpace(os.Getenv("TERM_PROGRAM_VERSION")); version != "" {
			return sanitizeTerminalToken(termProgram + "/" + version)
		}
		return sanitizeTerminalToken(termProgram)
	}
	if version := strings.TrimSpace(os.Getenv("WEZTERM_VERSION")); version != "" {
		return sanitizeTerminalToken("WezTerm/" + version)
	}
	if envExists("ITERM_SESSION_ID") || envExists("ITERM_PROFILE") || envExists("ITERM_PROFILE_NAME") {
		return "iTerm.app"
	}
	if envExists("TERM_SESSION_ID") {
		return "Apple_Terminal"
	}
	term := strings.TrimSpace(os.Getenv("TERM"))
	if envExists("KITTY_WINDOW_ID") || strings.Contains(strings.ToLower(term), "kitty") {
		return "kitty"
	}
	if envExists("ALACRITTY_SOCKET") || term == "alacritty" {
		return "Alacritty"
	}
	if version := strings.TrimSpace(os.Getenv("KONSOLE_VERSION")); version != "" {
		return sanitizeTerminalToken("Konsole/" + version)
	}
	if envExists("GNOME_TERMINAL_SCREEN") {
		return "gnome-terminal"
	}
	if version := strings.TrimSpace(os.Getenv("VTE_VERSION")); version != "" {
		return sanitizeTerminalToken("VTE/" + version)
	}
	if envExists("WT_SESSION") {
		return "WindowsTerminal"
	}
	if term != "" {
		return sanitizeTerminalToken(term)
	}
	return "unknown"
}

func sanitizeUserAgent(candidate, fallback string) string {
	if isValidHeaderValue(candidate) {
		return candidate
	}
	sanitized := sanitizePrintableASCII(candidate)
	if sanitized != "" && isValidHeaderValue(sanitized) {
		return sanitized
	}
	if isValidHeaderValue(fallback) {
		return fallback
	}
	return CodexDefaultOriginator
}

func sanitizePrintableASCII(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= ' ' && r <= '~' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func sanitizeTerminalToken(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '/' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func isValidHeaderValue(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if r < ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

func envExists(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) != ""
}
