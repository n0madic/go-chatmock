package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/limits"
	"github.com/n0madic/go-chatmock/internal/oauth"
	"github.com/n0madic/go-chatmock/internal/proxy"
)

//go:embed prompts/prompt.md
var promptMD string

//go:embed prompts/prompt_gpt5_codex.md
var promptGPT5CodexMD string

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: go-chatmock <command> [flags]")
		fmt.Fprintln(os.Stderr, "Commands: login, serve, info")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "login":
		os.Exit(cmdLogin())
	case "serve":
		os.Exit(cmdServe())
	case "info":
		os.Exit(cmdInfo())
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		fmt.Fprintln(os.Stderr, "Commands: login, serve, info")
		os.Exit(1)
	}
}

func cmdLogin() int {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	noBrowser := fs.Bool("no-browser", false, "Do not open the browser automatically")
	verbose := fs.Bool("verbose", false, "Enable verbose logging")
	fs.Parse(os.Args[2:])

	if config.ClientID() == "" {
		slog.Error("no OAuth client id configured; set CHATGPT_LOCAL_CLIENT_ID")
		return 1
	}

	bindHost := os.Getenv("CHATGPT_LOCAL_LOGIN_BIND")
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}

	srv, err := oauth.NewServer(bindHost, *verbose)
	if err != nil {
		slog.Error("failed to start OAuth server", "error", err)
		return 1
	}

	authURL := srv.AuthURL()
	slog.Info("starting local login server", "url", oauth.URLBase)

	if !*noBrowser {
		openBrowser(authURL)
	}
	fmt.Fprintf(os.Stderr, "If your browser did not open, navigate to:\n%s\n", authURL)

	// Stdin paste worker
	go stdinPasteWorker(srv)

	// Handle SIGINT
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nKeyboard interrupt received, exiting.")
		srv.Shutdown()
	}()

	if err := srv.ListenAndServe(); err != nil && err.Error() != "http: Server closed" {
		slog.Error("server error", "error", err)
	}

	return srv.ExitCode
}

func stdinPasteWorker(srv *oauth.Server) {
	fmt.Fprintln(os.Stderr, "If the browser can't reach this machine, paste the full redirect URL here and press Enter:")
	var line string
	fmt.Scanln(&line)
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	parsed, err := url.Parse(line)
	if err != nil {
		slog.Error("failed to parse pasted URL", "error", err)
		return
	}
	code := parsed.Query().Get("code")
	state := parsed.Query().Get("state")
	if code == "" {
		slog.Error("input did not contain an auth code")
		return
	}
	if state != "" && state != srv.State {
		slog.Error("state mismatch; ignoring pasted URL for safety")
		return
	}

	slog.Info("received redirect URL; completing login without callback")
	af, err := srv.ExchangeCode(context.Background(), code)
	if err != nil {
		slog.Error("failed to process pasted redirect URL", "error", err)
		return
	}
	if err := auth.WriteAuthFile(af); err != nil {
		slog.Error("unable to persist auth file", "error", err)
		return
	}
	srv.ExitCode = 0
	slog.Info("login successful; tokens saved")
	srv.Shutdown()
}

func cmdServe() int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfg := config.DefaultFromEnv()

	fs.StringVar(&cfg.Host, "host", cfg.Host, "Bind host")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "Listen port")
	fs.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "Enable verbose logging")
	fs.BoolVar(&cfg.VerboseObfuscation, "verbose-obfuscation", cfg.VerboseObfuscation, "Dump raw SSE events")
	fs.StringVar(&cfg.ReasoningEffort, "reasoning-effort", cfg.ReasoningEffort, "Reasoning effort level (minimal|low|medium|high|xhigh)")
	fs.StringVar(&cfg.ReasoningSummary, "reasoning-summary", cfg.ReasoningSummary, "Reasoning summary (auto|concise|detailed|none)")
	fs.StringVar(&cfg.ReasoningCompat, "reasoning-compat", cfg.ReasoningCompat, "Reasoning compat mode (think-tags|o3|legacy|current)")
	fs.StringVar(&cfg.DebugModel, "debug-model", cfg.DebugModel, "Force model name override")
	fs.BoolVar(&cfg.ExposeReasoningModels, "expose-reasoning-models", cfg.ExposeReasoningModels, "Expose effort variants as separate models")
	fs.BoolVar(&cfg.DefaultWebSearch, "enable-web-search", cfg.DefaultWebSearch, "Enable default web_search tool")
	fs.Parse(os.Args[2:])

	cfg.BaseInstructions = promptMD
	cfg.CodexInstructions = promptGPT5CodexMD

	srv := proxy.New(cfg)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nShutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	slog.Info("ChatMock starting", "host", cfg.Host, "port", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err.Error() != "http: Server closed" {
		slog.Error("server error", "error", err)
		return 1
	}
	return 0
}

func cmdInfo() int {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "Output raw auth.json contents")
	fs.Parse(os.Args[2:])

	af, err := auth.ReadAuthFile()
	if *jsonOut {
		if err != nil {
			fmt.Println("{}")
		} else {
			data, _ := json.MarshalIndent(af, "", "  ")
			fmt.Println(string(data))
		}
		return 0
	}

	tm := auth.NewTokenManager(config.ClientID(), config.TokenURL())
	accessToken, accountID, tokenErr := tm.GetEffectiveAuth()

	var idToken string
	if af != nil {
		idToken = af.Tokens.IDToken
	}

	if tokenErr != nil || accessToken == "" || idToken == "" {
		fmt.Println("\U0001F464 Account")
		fmt.Println("  \u2022 Not signed in")
		fmt.Println("  \u2022 Run: go-chatmock login")
		fmt.Println()
		printUsageLimits()
		return 0
	}

	idClaims, _ := auth.ParseJWTClaims(idToken)
	accessClaims, _ := auth.ParseJWTClaims(accessToken)

	email := claimString(idClaims, "email")
	if email == "" {
		email = claimString(idClaims, "preferred_username")
	}
	if email == "" {
		email = "<unknown>"
	}

	planRaw := "unknown"
	if authObj, ok := accessClaims["https://api.openai.com/auth"].(map[string]any); ok {
		if p, ok := authObj["chatgpt_plan_type"].(string); ok {
			planRaw = p
		}
	}
	planMap := map[string]string{
		"plus": "Plus", "pro": "Pro", "free": "Free", "team": "Team", "enterprise": "Enterprise",
	}
	plan := planMap[strings.ToLower(planRaw)]
	if plan == "" && planRaw != "" {
		plan = strings.ToUpper(planRaw[:1]) + planRaw[1:]
	}
	if plan == "" {
		plan = "Unknown"
	}

	fmt.Println("\U0001F464 Account")
	fmt.Println("  \u2022 Signed in with ChatGPT")
	fmt.Printf("  \u2022 Login: %s\n", email)
	fmt.Printf("  \u2022 Plan: %s\n", plan)
	if accountID != "" {
		fmt.Printf("  \u2022 Account ID: %s\n", accountID)
	}
	fmt.Println()
	printUsageLimits()
	return 0
}

func printUsageLimits() {
	fmt.Println("\U0001F4CA Usage Limits")

	stored := limits.LoadSnapshot()
	if stored == nil {
		fmt.Println("  No usage data available yet. Send a request through ChatMock first.")
		fmt.Println()
		return
	}

	fmt.Printf("Last updated: %s\n", formatLocalDateTime(stored.CapturedAt))
	fmt.Println()

	type windowInfo struct {
		icon   string
		desc   string
		window *limits.RateLimitWindow
	}
	var windows []windowInfo
	if stored.Snapshot.Primary != nil {
		windows = append(windows, windowInfo{"\u26A1", "5 hour limit", stored.Snapshot.Primary})
	}
	if stored.Snapshot.Secondary != nil {
		windows = append(windows, windowInfo{"\U0001F4C5", "Weekly limit", stored.Snapshot.Secondary})
	}

	if len(windows) == 0 {
		fmt.Println("  Usage data was captured but no limit windows were provided.")
		fmt.Println()
		return
	}

	for i, wi := range windows {
		if i > 0 {
			fmt.Println()
		}
		pct := clampPercent(wi.window.UsedPercent)
		remaining := 100.0 - pct
		if remaining < 0 {
			remaining = 0
		}
		color := usageColor(pct)
		reset := "\033[0m"
		bar := renderProgressBar(pct)

		fmt.Printf("%s %s\n", wi.icon, wi.desc)
		fmt.Printf("%s%s%s %s%5.1f%% used%s | %5.1f%% left\n", color, bar, reset, color, pct, reset, remaining)

		resetIn := formatResetDuration(wi.window.ResetsInSeconds)
		resetAt := limits.ComputeResetAt(stored.CapturedAt, wi.window)

		if resetIn != "" && resetAt != nil {
			fmt.Printf("    \u23F3 Resets in: %s at %s\n", resetIn, formatLocalDateTime(*resetAt))
		} else if resetIn != "" {
			fmt.Printf("    \u23F3 Resets in: %s\n", resetIn)
		} else if resetAt != nil {
			fmt.Printf("    \u23F3 Resets at: %s\n", formatLocalDateTime(*resetAt))
		}
	}
	fmt.Println()
}

const barSegments = 30

func renderProgressBar(pct float64) string {
	ratio := pct / 100.0
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filledExact := ratio * float64(barSegments)
	filled := int(filledExact)
	partial := filledExact - float64(filled)
	hasPartial := partial > 0.5
	if hasPartial {
		filled++
	}
	if filled > barSegments {
		filled = barSegments
	}
	empty := barSegments - filled
	var bar string
	if hasPartial && filled > 0 {
		bar = strings.Repeat("\u2588", filled-1) + "\u2593" + strings.Repeat("\u2591", empty)
	} else {
		bar = strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", empty)
	}
	return "[" + bar + "]"
}

func usageColor(pct float64) string {
	if pct >= 90 {
		return "\033[91m"
	} else if pct >= 75 {
		return "\033[93m"
	} else if pct >= 50 {
		return "\033[94m"
	}
	return "\033[92m"
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func formatLocalDateTime(t time.Time) string {
	local := t.Local()
	tz := local.Format("MST")
	return fmt.Sprintf("%s %s", local.Format("Jan 02, 2006 15:04"), tz)
}

func formatResetDuration(seconds *int) string {
	if seconds == nil {
		return ""
	}
	v := *seconds
	if v < 0 {
		v = 0
	}
	days := v / 86400
	v %= 86400
	hours := v / 3600
	v %= 3600
	minutes := v / 60
	v %= 60

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if len(parts) == 0 && v > 0 {
		parts = append(parts, "under 1m")
	}
	if len(parts) == 0 {
		parts = append(parts, "0m")
	}
	return strings.Join(parts, " ")
}

func claimString(claims map[string]any, key string) string {
	if claims == nil {
		return ""
	}
	v, _ := claims[key].(string)
	return v
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Warn("failed to open browser", "error", err)
	}
}
