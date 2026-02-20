package limits

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/n0madic/go-chatmock/internal/auth"
)

const limitsFilename = "usage_limits.json"

// RateLimitWindow represents a single rate limit window.
type RateLimitWindow struct {
	UsedPercent     float64 `json:"used_percent"`
	WindowMinutes   *int    `json:"window_minutes"`
	ResetsInSeconds *int    `json:"resets_in_seconds"`
}

// RateLimitSnapshot holds primary and secondary rate limit windows.
type RateLimitSnapshot struct {
	Primary   *RateLimitWindow `json:"primary,omitempty"`
	Secondary *RateLimitWindow `json:"secondary,omitempty"`
}

// StoredSnapshot includes a capture timestamp with the snapshot.
type StoredSnapshot struct {
	CapturedAt time.Time
	Snapshot   RateLimitSnapshot
}

// storedSnapshotDisk is the on-disk JSON format for a stored snapshot.
type storedSnapshotDisk struct {
	CapturedAt string           `json:"captured_at"`
	Primary    *RateLimitWindow `json:"primary,omitempty"`
	Secondary  *RateLimitWindow `json:"secondary,omitempty"`
}

// ParseHeaders extracts rate limit information from upstream response headers.
func ParseHeaders(headers http.Header) *RateLimitSnapshot {
	primary := parseWindow(headers,
		"x-codex-primary-used-percent",
		"x-codex-primary-window-minutes",
		"x-codex-primary-reset-after-seconds",
	)
	secondary := parseWindow(headers,
		"x-codex-secondary-used-percent",
		"x-codex-secondary-window-minutes",
		"x-codex-secondary-reset-after-seconds",
	)
	if primary == nil && secondary == nil {
		return nil
	}
	return &RateLimitSnapshot{Primary: primary, Secondary: secondary}
}

func parseWindow(headers http.Header, usedKey, windowKey, resetKey string) *RateLimitWindow {
	usedStr := headers.Get(usedKey)
	if usedStr == "" {
		return nil
	}
	used, err := strconv.ParseFloat(usedStr, 64)
	if err != nil || math.IsNaN(used) || math.IsInf(used, 0) {
		return nil
	}
	w := &RateLimitWindow{UsedPercent: used}
	if v := headers.Get(windowKey); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			w.WindowMinutes = &i
		}
	}
	if v := headers.Get(resetKey); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			w.ResetsInSeconds = &i
		}
	}
	return w
}

// limitsPath is a function variable so tests can override the path.
var limitsPath = func() string {
	return filepath.Join(auth.HomeDir(), limitsFilename)
}

// StoreSnapshot persists a rate limit snapshot to disk.
func StoreSnapshot(snapshot *RateLimitSnapshot, capturedAt time.Time) {
	if snapshot == nil {
		return
	}
	dir := auth.HomeDir()
	_ = os.MkdirAll(dir, 0o700)

	disk := storedSnapshotDisk{
		CapturedAt: capturedAt.UTC().Format(time.RFC3339),
		Primary:    snapshot.Primary,
		Secondary:  snapshot.Secondary,
	}

	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(limitsPath(), data, 0o600)
}

// LoadSnapshot reads a stored rate limit snapshot from disk.
func LoadSnapshot() *StoredSnapshot {
	data, err := os.ReadFile(limitsPath())
	if err != nil {
		return nil
	}
	var disk storedSnapshotDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil
	}

	if disk.CapturedAt == "" {
		return nil
	}
	captured, err := time.Parse(time.RFC3339, disk.CapturedAt)
	if err != nil {
		return nil
	}

	snapshot := RateLimitSnapshot{
		Primary:   disk.Primary,
		Secondary: disk.Secondary,
	}
	if snapshot.Primary == nil && snapshot.Secondary == nil {
		return nil
	}
	return &StoredSnapshot{CapturedAt: captured, Snapshot: snapshot}
}

// RecordFromResponse extracts and stores rate limits from an upstream HTTP response.
func RecordFromResponse(headers http.Header) {
	if headers == nil {
		return
	}
	snapshot := ParseHeaders(headers)
	if snapshot == nil {
		return
	}
	StoreSnapshot(snapshot, time.Now().UTC())
}

// ComputeResetAt calculates when a rate limit window will reset.
func ComputeResetAt(capturedAt time.Time, w *RateLimitWindow) *time.Time {
	if w == nil || w.ResetsInSeconds == nil {
		return nil
	}
	t := capturedAt.Add(time.Duration(*w.ResetsInSeconds) * time.Second)
	return &t
}
