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
	"github.com/n0madic/go-chatmock/internal/models"
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
	jsonOut := fs.Bool("json", false, "Output service info as JSON")
	fs.Parse(os.Args[2:])

	af, _ := auth.ReadAuthFile()
	tm := auth.NewTokenManager(config.ClientID(), config.TokenURL())
	out := buildInfoOutput(af, tm)

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			slog.Error("failed to encode JSON output", "error", err)
			return 1
		}
		return 0
	}

	printInfoText(out)
	return 0
}

func buildInfoOutput(af *auth.AuthFile, tm *auth.TokenManager) infoOutput {
	out := infoOutput{
		UsageLimits: buildUsageLimits(),
	}

	accessToken, accountID, tokenErr := tm.GetEffectiveAuth()
	var idToken string
	if af != nil {
		idToken = af.Tokens.IDToken
	}
	signedIn := tokenErr == nil && accessToken != "" && idToken != ""

	if !signedIn {
		out.Account = infoAccount{
			SignedIn: false,
			Message:  "Not signed in",
			Hint:     "Run: go-chatmock login",
		}
		return out
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

	out.Account = infoAccount{
		SignedIn:  true,
		Provider:  "ChatGPT",
		Login:     email,
		Plan:      plan,
		AccountID: accountID,
	}
	out.AvailableModels = buildModels(tm)
	return out
}

type infoOutput struct {
	Account         infoAccount     `json:"account"`
	AvailableModels *infoModels     `json:"available_models,omitempty"`
	UsageLimits     infoUsageLimits `json:"usage_limits"`
}

type infoAccount struct {
	SignedIn  bool   `json:"signed_in"`
	Provider  string `json:"provider,omitempty"`
	Login     string `json:"login,omitempty"`
	Plan      string `json:"plan,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	Message   string `json:"message,omitempty"`
	Hint      string `json:"hint,omitempty"`
}

type infoModels struct {
	Live    bool        `json:"live"`
	Message string      `json:"message,omitempty"`
	Models  []infoModel `json:"models"`
}

type infoModel struct {
	Slug             string   `json:"slug"`
	Description      string   `json:"description,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
}

type infoUsageLimits struct {
	LastUpdated        string            `json:"last_updated,omitempty"`
	LastUpdatedRFC3339 string            `json:"last_updated_rfc3339,omitempty"`
	Message            string            `json:"message,omitempty"`
	Windows            []infoUsageWindow `json:"windows,omitempty"`
}

type infoUsageWindow struct {
	Key              string  `json:"key"`
	Label            string  `json:"label"`
	UsedPercent      float64 `json:"used_percent"`
	RemainingPercent float64 `json:"remaining_percent"`
	WindowMinutes    *int    `json:"window_minutes,omitempty"`
	ResetsInSeconds  *int    `json:"resets_in_seconds,omitempty"`
	ResetsIn         string  `json:"resets_in,omitempty"`
	ResetsAt         string  `json:"resets_at,omitempty"`
	ResetsAtRFC3339  string  `json:"resets_at_rfc3339,omitempty"`
}

func buildModels(tm *auth.TokenManager) *infoModels {
	reg := models.NewRegistry(tm)
	mods := reg.GetModels()
	isLive := reg.IsPopulated()

	out := &infoModels{
		Live: isLive,
	}
	if !isLive {
		out.Message = "could not fetch from API, showing static list"
	}

	for _, m := range mods {
		if m.Visibility == "hidden" {
			continue
		}
		item := infoModel{
			Slug:        m.Slug,
			Description: m.Description,
		}
		for _, lvl := range m.SupportedReasoningLevels {
			item.ReasoningEfforts = append(item.ReasoningEfforts, lvl.Effort)
		}
		out.Models = append(out.Models, item)
	}

	return out
}

func buildUsageLimits() infoUsageLimits {
	stored := limits.LoadSnapshot()
	if stored == nil {
		return infoUsageLimits{
			Message: "No usage data available yet. Send a request through ChatMock first.",
		}
	}

	out := infoUsageLimits{
		LastUpdated:        formatLocalDateTime(stored.CapturedAt),
		LastUpdatedRFC3339: stored.CapturedAt.UTC().Format(time.RFC3339),
	}

	type windowInfo struct {
		key    string
		desc   string
		window *limits.RateLimitWindow
	}
	var windows []windowInfo
	if stored.Snapshot.Primary != nil {
		windows = append(windows, windowInfo{key: "primary", desc: "5 hour limit", window: stored.Snapshot.Primary})
	}
	if stored.Snapshot.Secondary != nil {
		windows = append(windows, windowInfo{key: "secondary", desc: "Weekly limit", window: stored.Snapshot.Secondary})
	}

	if len(windows) == 0 {
		out.Message = "Usage data was captured but no limit windows were provided."
		return out
	}

	for _, wi := range windows {
		pct := clampPercent(wi.window.UsedPercent)
		remaining := 100.0 - pct
		if remaining < 0 {
			remaining = 0
		}
		w := infoUsageWindow{
			Key:              wi.key,
			Label:            wi.desc,
			UsedPercent:      pct,
			RemainingPercent: remaining,
			WindowMinutes:    wi.window.WindowMinutes,
			ResetsInSeconds:  wi.window.ResetsInSeconds,
			ResetsIn:         formatResetDuration(wi.window.ResetsInSeconds),
		}
		if resetAt := limits.ComputeResetAt(stored.CapturedAt, wi.window); resetAt != nil {
			w.ResetsAt = formatLocalDateTime(*resetAt)
			w.ResetsAtRFC3339 = resetAt.UTC().Format(time.RFC3339)
		}
		out.Windows = append(out.Windows, w)
	}

	return out
}

func printInfoText(out infoOutput) {
	fmt.Println("\U0001F464 Account")
	if !out.Account.SignedIn {
		msg := out.Account.Message
		if msg == "" {
			msg = "Not signed in"
		}
		fmt.Printf("  \u2022 %s\n", msg)
		if out.Account.Hint != "" {
			fmt.Printf("  \u2022 %s\n", out.Account.Hint)
		}
		fmt.Println()
		printUsageLimitsText(out.UsageLimits)
		return
	}

	provider := out.Account.Provider
	if provider == "" {
		provider = "ChatGPT"
	}
	fmt.Printf("  \u2022 Signed in with %s\n", provider)
	if out.Account.Login != "" {
		fmt.Printf("  \u2022 Login: %s\n", out.Account.Login)
	}
	if out.Account.Plan != "" {
		fmt.Printf("  \u2022 Plan: %s\n", out.Account.Plan)
	}
	if out.Account.AccountID != "" {
		fmt.Printf("  \u2022 Account ID: %s\n", out.Account.AccountID)
	}
	fmt.Println()

	printAvailableModelsText(out.AvailableModels)
	printUsageLimitsText(out.UsageLimits)
}

func printAvailableModelsText(modelsInfo *infoModels) {
	if modelsInfo == nil {
		return
	}

	fmt.Println("\U0001F916 Available Models")
	if !modelsInfo.Live {
		msg := modelsInfo.Message
		if msg == "" {
			msg = "could not fetch from API, showing static list"
		}
		fmt.Printf("  (%s)\n", msg)
	}

	for _, m := range modelsInfo.Models {
		line := fmt.Sprintf("  \u2022 %-28s", m.Slug)
		if m.Description != "" {
			line += "  " + m.Description
		}
		if len(m.ReasoningEfforts) > 0 {
			line += "  [" + strings.Join(m.ReasoningEfforts, " ") + "]"
		}
		fmt.Println(line)
	}
	fmt.Println()
}

func printUsageLimitsText(usage infoUsageLimits) {
	fmt.Println("\U0001F4CA Usage Limits")
	if usage.LastUpdated != "" {
		fmt.Printf("Last updated: %s\n", usage.LastUpdated)
		fmt.Println()
	}
	if len(usage.Windows) == 0 {
		if usage.Message != "" {
			fmt.Printf("  %s\n", usage.Message)
		}
		fmt.Println()
		return
	}

	for i, w := range usage.Windows {
		if i > 0 {
			fmt.Println()
		}
		pct := clampPercent(w.UsedPercent)
		remaining := w.RemainingPercent
		if remaining < 0 {
			remaining = 0
		}
		color := usageColor(pct)
		reset := "\033[0m"
		bar := renderProgressBar(pct)

		fmt.Printf("%s %s\n", usageWindowIcon(w.Key), w.Label)
		fmt.Printf("%s%s%s %s%5.1f%% used%s | %5.1f%% left\n", color, bar, reset, color, pct, reset, remaining)

		if w.ResetsIn != "" && w.ResetsAt != "" {
			fmt.Printf("    \u23F3 Resets in: %s at %s\n", w.ResetsIn, w.ResetsAt)
		} else if w.ResetsIn != "" {
			fmt.Printf("    \u23F3 Resets in: %s\n", w.ResetsIn)
		} else if w.ResetsAt != "" {
			fmt.Printf("    \u23F3 Resets at: %s\n", w.ResetsAt)
		}
	}
	fmt.Println()
}

func usageWindowIcon(key string) string {
	switch key {
	case "primary":
		return "\u26A1"
	case "secondary":
		return "\U0001F4C5"
	default:
		return "\u2022"
	}
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
	v := max(*seconds, 0)
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
