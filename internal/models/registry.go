package models

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/config"
)

// cacheTTL is how long to cache the remote model list before background refresh.
const cacheTTL = 5 * time.Minute

// ReasoningLevel represents a supported reasoning effort level for a model.
type ReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// RemoteModel represents a model returned by the ChatGPT backend models endpoint.
type RemoteModel struct {
	Slug                     string           `json:"slug"`
	DisplayName              string           `json:"display_name"`
	Description              string           `json:"description"`
	DefaultReasoningLevel    string           `json:"default_reasoning_level"`
	SupportedReasoningLevels []ReasoningLevel `json:"supported_reasoning_levels"`
	Visibility               string           `json:"visibility"`
	SupportedInAPI           bool             `json:"supported_in_api"`
	Priority                 int              `json:"priority"`
}

type remoteModelsResponse struct {
	Models []RemoteModel `json:"models"`
}

type diskModelsCache struct {
	FetchedAt string        `json:"fetched_at"`
	ETag      string        `json:"etag"`
	Models    []RemoteModel `json:"models"`
}

// Registry fetches and caches the available model list from the upstream.
//
// Two mutexes are used intentionally:
//   - mu (RWMutex) guards the cached data and allows concurrent readers.
//   - fetchMu (Mutex) serializes network fetches so that when many requests
//     arrive simultaneously on an empty cache, only one goroutine hits the
//     upstream; the rest wait and then read the result already stored by the
//     first goroutine.
type Registry struct {
	mu        sync.RWMutex
	fetchMu   sync.Mutex
	tm        *auth.TokenManager
	models    []RemoteModel
	lastFetch time.Time
	etag      string
}

// modelsCachePath is a function variable so tests can override where warm cache
// is read from.
//
// The default path (~/.codex/models_cache.json) is shared with the official
// Codex CLI. Reading it at startup means the registry is pre-populated on the
// first request if the user has already run the CLI, avoiding a blocking
// network fetch on the hot path.
var modelsCachePath = func() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return filepath.Join(d, "models_cache.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "models_cache.json")
}

// NewRegistry creates a model registry backed by the given token manager and
// preloads models from a local Codex cache file when available.
func NewRegistry(tm *auth.TokenManager) *Registry {
	r := &Registry{tm: tm}
	loaded, missing := r.loadFromDiskCache()
	if !loaded && missing && tm != nil {
		go func() {
			r.fetchMu.Lock()
			defer r.fetchMu.Unlock()
			if err := r.doFetch(); err != nil {
				slog.Warn("initial models refresh failed after missing cache", "error", err)
			}
		}()
	}
	return r
}

// GetModels returns the cached remote model list, refreshing if needed.
// If no cache is available, first call blocks to fetch. On stale cache, refreshes
// in background and returns the cached value immediately. Falls back to the static
// catalog if the remote fetch fails or produces an empty list.
func (r *Registry) GetModels() []RemoteModel {
	r.mu.RLock()
	age := time.Since(r.lastFetch)
	cached := r.models
	r.mu.RUnlock()

	if len(cached) == 0 {
		// First call — synchronous fetch with deduplication.
		r.fetchMu.Lock()
		r.mu.RLock()
		cached = r.models
		r.mu.RUnlock()
		if len(cached) == 0 {
			if err := r.doFetch(); err != nil {
				slog.Warn("models fetch failed, using static fallback", "error", err)
			}
			r.mu.RLock()
			cached = r.models
			r.mu.RUnlock()
		}
		r.fetchMu.Unlock()

		if len(cached) == 0 {
			return StaticFallback()
		}
		return cached
	}

	if age >= cacheTTL {
		// Stale — refresh in background, return current cache now.
		go func() {
			r.fetchMu.Lock()
			defer r.fetchMu.Unlock()
			if err := r.doFetch(); err != nil {
				slog.Warn("background models refresh failed", "error", err)
			}
		}()
	}

	return cached
}

// Refresh forces an immediate synchronous fetch and returns the result.
// Returns the fetched models on success, or the static fallback on error.
func (r *Registry) Refresh() ([]RemoteModel, error) {
	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()
	err := r.doFetch()
	r.mu.RLock()
	result := r.models
	r.mu.RUnlock()
	if len(result) == 0 {
		return StaticFallback(), err
	}
	return result, err
}

// IsPopulated reports whether the registry has remote data (not just static fallback).
func (r *Registry) IsPopulated() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.models) > 0
}

// IsKnownModel checks whether slug is in the populated registry.
// Returns (true, "") if the registry is empty — permissive when credentials not yet
// available. Returns (false, hint) if the registry is populated but slug is not found,
// where hint is a comma-separated list of available model slugs.
func (r *Registry) IsKnownModel(slug string) (bool, string) {
	r.mu.RLock()
	mods := r.models
	r.mu.RUnlock()

	if len(mods) == 0 {
		return true, ""
	}

	for _, m := range mods {
		if m.Slug == slug {
			return true, ""
		}
	}

	var names []string
	for _, m := range mods {
		if m.Visibility != "hidden" {
			names = append(names, m.Slug)
		}
	}
	return false, strings.Join(names, ", ")
}

// doFetch performs the actual HTTP GET to the models endpoint with ETag caching.
// Caller must hold fetchMu.
func (r *Registry) doFetch() error {
	accessToken, accountID, err := r.tm.GetEffectiveAuth()
	if err != nil || accessToken == "" {
		return fmt.Errorf("no credentials available")
	}

	url := fmt.Sprintf("%s?client_version=%s", config.ModelsURL, config.CodexClientVersion)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	config.ApplyCodexDefaultHeaders(req.Header)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}

	r.mu.RLock()
	etag := r.etag
	r.mu.RUnlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("models fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		r.mu.Lock()
		r.lastFetch = time.Now()
		r.mu.Unlock()
		r.saveToDiskCache()
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("models endpoint returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var mr remoteModelsResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return fmt.Errorf("failed to parse models response: %w", err)
	}

	newEtag := resp.Header.Get("ETag")

	r.mu.Lock()
	r.models = mr.Models
	r.lastFetch = time.Now()
	if newEtag != "" {
		r.etag = newEtag
	}
	r.mu.Unlock()

	r.saveToDiskCache()

	return nil
}

func (r *Registry) saveToDiskCache() {
	path := modelsCachePath()
	if path == "" {
		return
	}

	r.mu.RLock()
	cache := diskModelsCache{
		FetchedAt: r.lastFetch.UTC().Format(time.RFC3339Nano),
		ETag:      r.etag,
		Models:    r.models,
	}
	r.mu.RUnlock()

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		slog.Warn("failed to marshal models cache", "error", err)
		return
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("failed to create models cache directory", "error", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		slog.Warn("failed to write models cache", "error", err)
	}
}

func (r *Registry) loadFromDiskCache() (loaded bool, missing bool) {
	path := modelsCachePath()
	if path == "" {
		return false, true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, true
		}
		return false, false
	}

	var cache diskModelsCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return false, false
	}
	if len(cache.Models) == 0 {
		return false, false
	}

	var fetchedAt time.Time
	if cache.FetchedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, cache.FetchedAt)
		if err == nil {
			fetchedAt = parsed
		}
	}

	r.mu.Lock()
	r.models = cache.Models
	r.lastFetch = fetchedAt
	if cache.ETag != "" {
		r.etag = cache.ETag
	}
	r.mu.Unlock()

	return true, false
}

// StaticFallback converts the static catalog to a []RemoteModel slice.
// It is used when credentials are unavailable or the upstream is unreachable,
// so the /v1/models endpoint and model validation still return useful results
// without requiring a successful authentication flow.
func StaticFallback() []RemoteModel {
	var out []RemoteModel
	for _, g := range AllModelGroups() {
		rm := RemoteModel{
			Slug:           g.Base,
			DisplayName:    g.Base,
			Visibility:     "list",
			SupportedInAPI: true,
		}
		for _, e := range g.Efforts {
			rm.SupportedReasoningLevels = append(rm.SupportedReasoningLevels, ReasoningLevel{Effort: e})
		}
		out = append(out, rm)
	}
	return out
}
