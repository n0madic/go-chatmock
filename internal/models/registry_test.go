package models

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewRegistryLoadsDiskCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models_cache.json")

	cache := `{
  "fetched_at": "` + time.Now().UTC().Format(time.RFC3339Nano) + `",
  "etag": "W/\"abc\"",
  "models": [
    {
      "slug": "gpt-cache",
      "display_name": "gpt-cache",
      "visibility": "list",
      "supported_reasoning_levels": [{"effort": "medium"}]
    }
  ]
}`
	if err := os.WriteFile(path, []byte(cache), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	origPath := modelsCachePath
	modelsCachePath = func() string { return path }
	defer func() { modelsCachePath = origPath }()

	r := NewRegistry(nil)
	if !r.IsPopulated() {
		t.Fatal("expected registry to be populated from disk cache")
	}

	mods := r.GetModels()
	if len(mods) != 1 {
		t.Fatalf("expected 1 model, got %d", len(mods))
	}
	if mods[0].Slug != "gpt-cache" {
		t.Fatalf("expected slug gpt-cache, got %q", mods[0].Slug)
	}
	if r.etag != "W/\"abc\"" {
		t.Fatalf("expected etag from cache, got %q", r.etag)
	}
}

func TestNewRegistryMissingDiskCache(t *testing.T) {
	origPath := modelsCachePath
	modelsCachePath = func() string { return filepath.Join(t.TempDir(), "missing.json") }
	defer func() { modelsCachePath = origPath }()

	r := NewRegistry(nil)
	if r.IsPopulated() {
		t.Fatal("expected empty registry when cache file is missing")
	}
}

func TestNewRegistryIgnoresInvalidDiskCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models_cache.json")
	if err := os.WriteFile(path, []byte("{invalid"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	origPath := modelsCachePath
	modelsCachePath = func() string { return path }
	defer func() { modelsCachePath = origPath }()

	r := NewRegistry(nil)
	if r.IsPopulated() {
		t.Fatal("expected empty registry for invalid cache JSON")
	}
}
