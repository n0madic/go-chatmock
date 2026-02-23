package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strings"
)

const (
	// CodexOriginator matches Codex CLI default originator naming.
	CodexOriginator = "codex_cli_rs"
	// CodexClientVersion is sent to upstream to match Codex CLI behavior.
	CodexClientVersion = "0.104.0"
)

// CodexUserAgent builds a Codex CLI-style User-Agent string:
// codex_cli_rs/<version> (<os> <arch>) <terminal> [sess-<hash>]
func CodexUserAgent() string {
	ua := fmt.Sprintf("%s/%s (%s %s) %s",
		CodexOriginator,
		CodexClientVersion,
		runtime.GOOS,
		runtime.GOARCH,
		codexTerminalUserAgent(),
	)
	if sessionID := strings.TrimSpace(os.Getenv("CODEX_SESSION_ID")); sessionID != "" {
		ua += " sess-" + hashStr(sessionID)
	}
	return ua
}

func codexTerminalUserAgent() string {
	if termProgram := strings.TrimSpace(os.Getenv("TERM_PROGRAM")); termProgram != "" {
		return termProgram + "/unknown"
	}
	if term := strings.TrimSpace(os.Getenv("TERM")); term != "" {
		return "term/" + term
	}
	return "unknown"
}

func hashStr(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}
