package models

import (
	"encoding/json"
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

func TestSaveToDiskCacheWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models_cache.json")

	origPath := modelsCachePath
	modelsCachePath = func() string { return path }
	defer func() { modelsCachePath = origPath }()

	r := &Registry{}
	r.models = []RemoteModel{
		{Slug: "gpt-test", DisplayName: "gpt-test", Visibility: "list", SupportedInAPI: true},
	}
	r.lastFetch = time.Now()
	r.etag = "W/\"test\""

	r.saveToDiskCache()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected cache file to exist: %v", err)
	}

	var cache diskModelsCache
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatalf("invalid cache JSON: %v", err)
	}
	if len(cache.Models) != 1 || cache.Models[0].Slug != "gpt-test" {
		t.Fatalf("unexpected cache content: %+v", cache)
	}
	if cache.ETag != "W/\"test\"" {
		t.Fatalf("unexpected etag: %q", cache.ETag)
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
