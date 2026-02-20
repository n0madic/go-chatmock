package limits

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeHeaders(pairs ...string) http.Header {
	h := make(http.Header)
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

// TestParseHeadersBothWindows verifies that both primary and secondary windows are parsed.
func TestParseHeadersBothWindows(t *testing.T) {
	h := makeHeaders(
		"x-codex-primary-used-percent", "42.5",
		"x-codex-primary-window-minutes", "60",
		"x-codex-primary-reset-after-seconds", "3600",
		"x-codex-secondary-used-percent", "10.0",
		"x-codex-secondary-window-minutes", "1440",
		"x-codex-secondary-reset-after-seconds", "86400",
	)

	snap := ParseHeaders(h)
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.Primary == nil {
		t.Fatal("expected non-nil primary window")
	}
	if snap.Secondary == nil {
		t.Fatal("expected non-nil secondary window")
	}
	if snap.Primary.UsedPercent != 42.5 {
		t.Errorf("primary used percent: got %v, want 42.5", snap.Primary.UsedPercent)
	}
	if snap.Secondary.UsedPercent != 10.0 {
		t.Errorf("secondary used percent: got %v, want 10.0", snap.Secondary.UsedPercent)
	}
	if snap.Primary.WindowMinutes == nil || *snap.Primary.WindowMinutes != 60 {
		t.Errorf("primary window minutes: got %v, want 60", snap.Primary.WindowMinutes)
	}
	if snap.Primary.ResetsInSeconds == nil || *snap.Primary.ResetsInSeconds != 3600 {
		t.Errorf("primary resets in seconds: got %v, want 3600", snap.Primary.ResetsInSeconds)
	}
}

// TestParseHeadersMissingSecondary verifies that a missing secondary header returns nil secondary.
func TestParseHeadersMissingSecondary(t *testing.T) {
	h := makeHeaders(
		"x-codex-primary-used-percent", "75.0",
	)

	snap := ParseHeaders(h)
	if snap == nil {
		t.Fatal("expected non-nil snapshot (primary present)")
	}
	if snap.Primary == nil {
		t.Error("expected non-nil primary window")
	}
	if snap.Secondary != nil {
		t.Errorf("expected nil secondary window, got %+v", snap.Secondary)
	}
}

// TestParseHeadersNonePresent verifies that absent rate-limit headers return nil.
func TestParseHeadersNonePresent(t *testing.T) {
	h := makeHeaders("Content-Type", "application/json")

	snap := ParseHeaders(h)
	if snap != nil {
		t.Errorf("expected nil snapshot, got %+v", snap)
	}
}

// TestParseHeadersInvalidFloat verifies that a non-numeric used-percent is ignored.
func TestParseHeadersInvalidFloat(t *testing.T) {
	h := makeHeaders("x-codex-primary-used-percent", "not-a-number")

	snap := ParseHeaders(h)
	if snap != nil {
		t.Errorf("expected nil snapshot for invalid float, got %+v", snap)
	}
}

// TestParseHeadersNaNPercent verifies that NaN used-percent is rejected.
func TestParseHeadersNaNPercent(t *testing.T) {
	h := makeHeaders("x-codex-primary-used-percent", "NaN")

	snap := ParseHeaders(h)
	if snap != nil {
		t.Errorf("expected nil snapshot for NaN percent, got %+v", snap)
	}
}

// TestParseHeadersInfPercent verifies that Inf used-percent is rejected.
func TestParseHeadersInfPercent(t *testing.T) {
	h := makeHeaders("x-codex-primary-used-percent", "+Inf")

	snap := ParseHeaders(h)
	if snap != nil {
		t.Errorf("expected nil snapshot for Inf percent, got %+v", snap)
	}
}

// TestStoreAndLoadRoundTrip verifies that a snapshot written to disk can be read back.
func TestStoreAndLoadRoundTrip(t *testing.T) {
	// Override the home directory used by the package to a temp directory.
	dir := t.TempDir()
	origPath := limitsPath
	limitsPath = func() string { return filepath.Join(dir, limitsFilename) }
	defer func() { limitsPath = origPath }()

	wm := 60
	rs := 3600
	snap := &RateLimitSnapshot{
		Primary: &RateLimitWindow{
			UsedPercent:     55.5,
			WindowMinutes:   &wm,
			ResetsInSeconds: &rs,
		},
	}
	capturedAt := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	StoreSnapshot(snap, capturedAt)

	loaded := LoadSnapshot()
	if loaded == nil {
		t.Fatal("LoadSnapshot returned nil after store")
	}
	if !loaded.CapturedAt.Equal(capturedAt) {
		t.Errorf("CapturedAt: got %v, want %v", loaded.CapturedAt, capturedAt)
	}
	if loaded.Snapshot.Primary == nil {
		t.Fatal("loaded primary window is nil")
	}
	if loaded.Snapshot.Primary.UsedPercent != 55.5 {
		t.Errorf("UsedPercent: got %v, want 55.5", loaded.Snapshot.Primary.UsedPercent)
	}
	if loaded.Snapshot.Primary.WindowMinutes == nil || *loaded.Snapshot.Primary.WindowMinutes != 60 {
		t.Errorf("WindowMinutes: got %v, want 60", loaded.Snapshot.Primary.WindowMinutes)
	}
}

// TestLoadSnapshotMissingFile verifies that LoadSnapshot returns nil when file is absent.
func TestLoadSnapshotMissingFile(t *testing.T) {
	dir := t.TempDir()
	origPath := limitsPath
	limitsPath = func() string { return filepath.Join(dir, "nonexistent.json") }
	defer func() { limitsPath = origPath }()

	if got := LoadSnapshot(); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// TestLoadSnapshotCorruptFile verifies that LoadSnapshot returns nil on bad JSON.
func TestLoadSnapshotCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, limitsFilename)
	_ = os.WriteFile(path, []byte("not valid json"), 0o600)

	origPath := limitsPath
	limitsPath = func() string { return path }
	defer func() { limitsPath = origPath }()

	if got := LoadSnapshot(); got != nil {
		t.Errorf("expected nil for corrupt file, got %+v", got)
	}
}

// TestStoreSnapshotNilIsNoOp verifies that StoreSnapshot(nil, ...) does not create a file.
func TestStoreSnapshotNilIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, limitsFilename)

	origPath := limitsPath
	limitsPath = func() string { return path }
	defer func() { limitsPath = origPath }()

	StoreSnapshot(nil, time.Now())

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected no file to be created, but it exists")
	}
}

// TestComputeResetAt verifies reset-time calculation.
func TestComputeResetAt(t *testing.T) {
	rs := 120
	w := &RateLimitWindow{ResetsInSeconds: &rs}
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	resetAt := ComputeResetAt(base, w)
	if resetAt == nil {
		t.Fatal("expected non-nil reset time")
	}
	want := base.Add(120 * time.Second)
	if !resetAt.Equal(want) {
		t.Errorf("reset time: got %v, want %v", resetAt, want)
	}
}

// TestComputeResetAtNilWindow verifies nil window returns nil.
func TestComputeResetAtNilWindow(t *testing.T) {
	base := time.Now()
	if got := ComputeResetAt(base, nil); got != nil {
		t.Errorf("expected nil for nil window, got %v", got)
	}
}

// TestComputeResetAtNoSeconds verifies nil ResetsInSeconds returns nil.
func TestComputeResetAtNoSeconds(t *testing.T) {
	w := &RateLimitWindow{UsedPercent: 50}
	if got := ComputeResetAt(time.Now(), w); got != nil {
		t.Errorf("expected nil when ResetsInSeconds is nil, got %v", got)
	}
}

// TestJSONRoundTripPreservesOptionalFields ensures optional int fields survive JSON encoding.
func TestJSONRoundTripPreservesOptionalFields(t *testing.T) {
	wm := 30
	w := RateLimitWindow{UsedPercent: 25.0, WindowMinutes: &wm}
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RateLimitWindow
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.WindowMinutes == nil || *got.WindowMinutes != 30 {
		t.Errorf("WindowMinutes: got %v, want 30", got.WindowMinutes)
	}
	if got.ResetsInSeconds != nil {
		t.Errorf("ResetsInSeconds should be nil when not set, got %v", got.ResetsInSeconds)
	}
}
