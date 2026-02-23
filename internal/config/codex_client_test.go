package config

import (
	"runtime"
	"strings"
	"testing"
)

func TestCodexUserAgentUsesTermProgram(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "WarpTerminal")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CODEX_SESSION_ID", "")

	ua := CodexUserAgent()

	if !strings.Contains(ua, "codex_cli_rs/"+CodexClientVersion) {
		t.Fatalf("ua missing codex prefix/version: %q", ua)
	}
	if !strings.Contains(ua, "("+runtime.GOOS+" "+runtime.GOARCH+")") {
		t.Fatalf("ua missing platform tuple: %q", ua)
	}
	if !strings.Contains(ua, "WarpTerminal/unknown") {
		t.Fatalf("ua missing TERM_PROGRAM marker: %q", ua)
	}
}

func TestCodexUserAgentFallsBackToTerm(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CODEX_SESSION_ID", "")

	ua := CodexUserAgent()
	if !strings.Contains(ua, "term/xterm-256color") {
		t.Fatalf("ua missing TERM fallback: %q", ua)
	}
}

func TestCodexUserAgentUsesUnknownTerminal(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TERM", "")
	t.Setenv("CODEX_SESSION_ID", "")

	ua := CodexUserAgent()
	if !strings.HasSuffix(ua, " unknown") {
		t.Fatalf("ua missing unknown terminal marker: %q", ua)
	}
}

func TestCodexUserAgentAddsSessionHash(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TERM", "")
	t.Setenv("CODEX_SESSION_ID", "abc")

	ua := CodexUserAgent()
	if !strings.HasSuffix(ua, " unknown sess-ba7816bf") {
		t.Fatalf("ua missing session hash suffix: %q", ua)
	}
}
