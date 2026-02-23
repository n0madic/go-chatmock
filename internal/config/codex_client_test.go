package config

import (
	"net/http"
	"strings"
	"testing"
)

func clearTerminalMarkers(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"WEZTERM_VERSION",
		"ITERM_SESSION_ID",
		"ITERM_PROFILE",
		"ITERM_PROFILE_NAME",
		"TERM_SESSION_ID",
		"KITTY_WINDOW_ID",
		"ALACRITTY_SOCKET",
		"KONSOLE_VERSION",
		"GNOME_TERMINAL_SCREEN",
		"VTE_VERSION",
		"WT_SESSION",
	} {
		t.Setenv(key, "")
	}
}

func TestCodexUserAgentUsesTermProgram(t *testing.T) {
	clearTerminalMarkers(t)
	t.Setenv("TERM_PROGRAM", "WarpTerminal")
	t.Setenv("TERM_PROGRAM_VERSION", "v1.2.3")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv(originatorOverrideEnv, "")

	ua := CodexUserAgent()

	if !strings.Contains(ua, CodexDefaultOriginator+"/"+CodexClientVersion) {
		t.Fatalf("ua missing codex prefix/version: %q", ua)
	}
	if !strings.Contains(ua, "; ") {
		t.Fatalf("ua missing codex os/arch separator: %q", ua)
	}
	if !strings.HasSuffix(ua, " WarpTerminal/v1.2.3") {
		t.Fatalf("ua missing TERM_PROGRAM marker: %q", ua)
	}
}

func TestCodexUserAgentFallsBackToTermCapability(t *testing.T) {
	clearTerminalMarkers(t)
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TERM_PROGRAM_VERSION", "")
	t.Setenv("TERM", "xterm-256color")

	ua := CodexUserAgent()
	if !strings.HasSuffix(ua, " xterm-256color") {
		t.Fatalf("ua missing TERM fallback: %q", ua)
	}
}

func TestCodexUserAgentUsesUnknownTerminal(t *testing.T) {
	clearTerminalMarkers(t)
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TERM_PROGRAM_VERSION", "")
	t.Setenv("TERM", "")

	ua := CodexUserAgent()
	if !strings.HasSuffix(ua, " unknown") {
		t.Fatalf("ua missing unknown terminal marker: %q", ua)
	}
}

func TestCodexOriginatorUsesOverrideWhenValid(t *testing.T) {
	clearTerminalMarkers(t)
	t.Setenv(originatorOverrideEnv, "codex_vscode")
	if got := CodexOriginator(); got != "codex_vscode" {
		t.Fatalf("CodexOriginator()=%q, want codex_vscode", got)
	}
}

func TestCodexOriginatorFallsBackWhenInvalid(t *testing.T) {
	clearTerminalMarkers(t)
	t.Setenv(originatorOverrideEnv, "bad\rvalue")
	if got := CodexOriginator(); got != CodexDefaultOriginator {
		t.Fatalf("CodexOriginator()=%q, want %q", got, CodexDefaultOriginator)
	}
}

func TestApplyCodexDefaultHeaders(t *testing.T) {
	clearTerminalMarkers(t)
	t.Setenv(originatorOverrideEnv, "")
	t.Setenv("TERM_PROGRAM", "vscode")
	t.Setenv("TERM_PROGRAM_VERSION", "1.99.0")
	t.Setenv("OPENAI_ORGANIZATION", "org_123")
	t.Setenv("OPENAI_PROJECT", "proj_456")

	h := http.Header{}
	ApplyCodexDefaultHeaders(h)

	if got := h.Get("originator"); got != CodexDefaultOriginator {
		t.Fatalf("originator=%q, want %q", got, CodexDefaultOriginator)
	}
	if got := h.Get("version"); got != CodexClientVersion {
		t.Fatalf("version=%q, want %q", got, CodexClientVersion)
	}
	if got := h.Get("OpenAI-Organization"); got != "org_123" {
		t.Fatalf("OpenAI-Organization=%q, want org_123", got)
	}
	if got := h.Get("OpenAI-Project"); got != "proj_456" {
		t.Fatalf("OpenAI-Project=%q, want proj_456", got)
	}
	if got := h.Get("User-Agent"); !strings.Contains(got, CodexDefaultOriginator+"/"+CodexClientVersion) {
		t.Fatalf("User-Agent=%q missing codex prefix/version", got)
	}
}
