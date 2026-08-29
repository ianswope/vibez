package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ── isNewer ───────────────────────────────────────────────────────────────────

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.0.10", "v0.0.9", true},  // integer, not lexicographic
		{"v0.0.9", "v0.0.9", false},  // same version
		{"v0.0.8", "v0.0.9", false},  // older
		{"v1.0.0", "v0.9.9", true},   // major bump
		{"v0.1.0", "v0.0.9", true},   // minor bump
		{"0.0.10", "0.0.9", true},    // no leading v
		{"v0.0.9", "v0.0.10", false}, // current is newer
	}
	for _, tc := range cases {
		got := isNewer(tc.latest, tc.current)
		if got != tc.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

// ── verifyChecksum ────────────────────────────────────────────────────────────

func TestVerifyChecksum_Valid(t *testing.T) {
	data := []byte("fake tarball content")
	sum := sha256.Sum256(data)
	hashHex := hex.EncodeToString(sum[:])
	assetName := "vibez_linux_amd64.tar.gz"

	checksumBody := hashHex + "  " + assetName + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksumBody))
	}))
	defer srv.Close()

	tarPath := filepath.Join(t.TempDir(), assetName)
	if err := os.WriteFile(tarPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := verifyChecksum(tarPath, assetName, srv.URL); err != nil {
		t.Errorf("verifyChecksum unexpectedly failed: %v", err)
	}
}

func TestVerifyChecksum_Mismatch(t *testing.T) {
	data := []byte("tampered content")
	assetName := "vibez_linux_amd64.tar.gz"

	checksumBody := "0000000000000000000000000000000000000000000000000000000000000000  " + assetName + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(checksumBody))
	}))
	defer srv.Close()

	tarPath := filepath.Join(t.TempDir(), assetName)
	if err := os.WriteFile(tarPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := verifyChecksum(tarPath, assetName, srv.URL); err == nil {
		t.Error("verifyChecksum should fail on hash mismatch")
	}
}

// ── extractBinary ─────────────────────────────────────────────────────────────

func TestExtractBinary_Found(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho vibez")
	tarPath := filepath.Join(t.TempDir(), "vibez_linux_amd64.tar.gz")

	f, err := os.Create(tarPath) //nolint:gosec // tarPath is a path inside t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "vibez",
		Typeflag: tar.TypeReg,
		Size:     int64(len(binaryContent)),
		Mode:     0o755,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binaryContent); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "vibez")
	if err := extractBinary(tarPath, "vibez", dst); err != nil {
		t.Fatalf("extractBinary failed: %v", err)
	}
	got, err := os.ReadFile(dst) //nolint:gosec // dst is a path inside t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(binaryContent) {
		t.Errorf("extracted content = %q, want %q", got, binaryContent)
	}
}

func TestExtractBinary_NotFound(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "empty.tar.gz")

	f, err := os.Create(tarPath) //nolint:gosec // tarPath is a path inside t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "vibez")
	if err := extractBinary(tarPath, "vibez", dst); err == nil {
		t.Error("extractBinary should error when binary not in archive")
	}
}

// ── shouldCheck / markChecked ─────────────────────────────────────────────────

func TestShouldCheck_NoStamp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	if !shouldCheck() {
		t.Error("shouldCheck should return true when stamp does not exist")
	}
}

func TestShouldCheck_RecentStamp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(dir, "last_update_check")
	if err := os.WriteFile(stamp, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if shouldCheck() {
		t.Error("shouldCheck should return false when stamp was just written")
	}
}

// ── update ────────────────────────────────────────────────────────────────────

// tarGz builds a gzipped tar holding one regular file.
func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0o755,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeReleases serves a releases endpoint for tag, plus the archive and
// checksums it advertises. withAsset says whether an archive for this platform
// is published at all, which is how a release that skips a platform is
// modelled.
func fakeReleases(t *testing.T, tag string, withAsset bool, binary []byte) *httptest.Server {
	t.Helper()
	assetName := fmt.Sprintf("vibez_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := tarGz(t, "vibez", binary)
	sum := sha256.Sum256(archive)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/checksums.txt":
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + assetName + "\n"))
		case "/asset":
			_, _ = w.Write(archive)
		default:
			assets := []ghAsset{{Name: "checksums.txt", BrowserDownloadURL: base + "/checksums.txt"}}
			if withAsset {
				assets = append(assets, ghAsset{Name: assetName, BrowserDownloadURL: base + "/asset"})
			}
			_ = json.NewEncoder(w).Encode(ghRelease{TagName: tag, Assets: assets})
		}
	}))
}

// isolateCache points the update-check stamp at a temp HOME so a test never
// reads or writes the real one.
func isolateCache(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
}

// fakeExe writes a stand-in binary and points executable() at it, so a test
// never overwrites the test binary itself.
func fakeExe(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vibez")
	if err := os.WriteFile(path, []byte("old binary"), mode); err != nil {
		t.Fatal(err)
	}
	orig := executable
	executable = func() (string, error) { return path, nil }
	t.Cleanup(func() { executable = orig })
	return path
}

func discard(string) {}

func TestUpdate_CurrentVersionNeedsNothing(t *testing.T) {
	isolateCache(t)
	srv := fakeReleases(t, "v1.2.3", true, []byte("new binary"))
	defer srv.Close()

	got := update(srv.URL, "v1.2.3", true, discard)
	if got.Outcome != OutcomeCurrent {
		t.Errorf("Outcome = %v, want OutcomeCurrent", got.Outcome)
	}
	if got.Exe != "" {
		t.Errorf("Exe = %q, want empty", got.Exe)
	}
}

func TestUpdate_CheckFailureKnowsNothing(t *testing.T) {
	isolateCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	got := update(srv.URL, "v1.0.0", true, discard)
	if got.Outcome != OutcomeFailed {
		t.Errorf("Outcome = %v, want OutcomeFailed", got.Outcome)
	}
	if got.Tag != "" {
		t.Errorf("Tag = %q, want empty: a failed check learns no version", got.Tag)
	}
	if got.Advice() != "" {
		t.Errorf("Advice = %q, want empty", got.Advice())
	}
}

func TestUpdate_DisabledStillReportsTheRelease(t *testing.T) {
	isolateCache(t)
	srv := fakeReleases(t, "v2.0.0", true, []byte("new binary"))
	defer srv.Close()

	got := update(srv.URL, "v1.0.0", false, discard)
	if got.Outcome != OutcomeDisabled {
		t.Errorf("Outcome = %v, want OutcomeDisabled", got.Outcome)
	}
	if got.Tag != "v2.0.0" {
		t.Errorf("Tag = %q, want v2.0.0", got.Tag)
	}
	if !strings.Contains(got.Advice(), "--no-update") {
		t.Errorf("Advice = %q, should say how to install it", got.Advice())
	}
}

// A platform with no published archive - linux/arm64 at the time of writing -
// must not be told to drop --no-update for a build that is not there.
func TestUpdate_MissingPlatformAssetIsManual(t *testing.T) {
	isolateCache(t)
	srv := fakeReleases(t, "v2.0.0", false, nil)
	defer srv.Close()

	for _, install := range []bool{true, false} {
		got := update(srv.URL, "v1.0.0", install, discard)
		if got.Outcome != OutcomeManual {
			t.Errorf("install=%v: Outcome = %v, want OutcomeManual", install, got.Outcome)
		}
		if got.Tag != "v2.0.0" {
			t.Errorf("install=%v: Tag = %q, want v2.0.0", install, got.Tag)
		}
	}
}

func TestUpdate_UnwritableBinaryIsManual(t *testing.T) {
	isolateCache(t)
	fakeExe(t, 0o400)
	srv := fakeReleases(t, "v2.0.0", true, []byte("new binary"))
	defer srv.Close()

	got := update(srv.URL, "v1.0.0", true, discard)
	if got.Outcome != OutcomeManual {
		t.Errorf("Outcome = %v, want OutcomeManual", got.Outcome)
	}
	if !strings.Contains(got.Advice(), "reinstall") {
		t.Errorf("Advice = %q, should point at reinstalling", got.Advice())
	}
}

func TestUpdate_InstallsOverTheRunningBinary(t *testing.T) {
	isolateCache(t)
	exe := fakeExe(t, 0o755)
	srv := fakeReleases(t, "v2.0.0", true, []byte("new binary"))
	defer srv.Close()

	got := update(srv.URL, "v1.0.0", true, discard)
	if got.Outcome != OutcomeInstalled {
		t.Fatalf("Outcome = %v, want OutcomeInstalled", got.Outcome)
	}

	// The path comes back symlink-resolved, which on macOS means /private/var
	// rather than the /var the test handed in.
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	if got.Exe != want {
		t.Errorf("Exe = %q, want %q", got.Exe, want)
	}
	content, err := os.ReadFile(exe) //nolint:gosec // exe is a path inside t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new binary" {
		t.Errorf("installed content = %q, want %q", content, "new binary")
	}
	if left, err := os.Stat(exe + ".new"); err == nil {
		t.Errorf("left a staging file behind: %s", left.Name())
	}
}

func TestUpdate_ChecksumMismatchInstallsNothing(t *testing.T) {
	isolateCache(t)
	exe := fakeExe(t, 0o755)
	assetName := fmt.Sprintf("vibez_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/checksums.txt":
			_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  " + assetName + "\n"))
		case "/asset":
			_, _ = w.Write(tarGz(t, "vibez", []byte("new binary")))
		default:
			_ = json.NewEncoder(w).Encode(ghRelease{TagName: "v2.0.0", Assets: []ghAsset{
				{Name: "checksums.txt", BrowserDownloadURL: base + "/checksums.txt"},
				{Name: assetName, BrowserDownloadURL: base + "/asset"},
			}})
		}
	}))
	defer srv.Close()

	got := update(srv.URL, "v1.0.0", true, discard)
	if got.Outcome != OutcomeManual {
		t.Errorf("Outcome = %v, want OutcomeManual", got.Outcome)
	}
	content, err := os.ReadFile(exe) //nolint:gosec // exe is a path inside t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old binary" {
		t.Errorf("binary was replaced despite a bad checksum: %q", content)
	}
}

// UpdateNow must ask even when CheckAndUpdate would have skipped the day's
// check, since the caller already knows this build cannot work.
func TestUpdateNow_IgnoresTheDailyWindow(t *testing.T) {
	isolateCache(t)
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "last_update_check"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if shouldCheck() {
		t.Fatal("stamp was just written, so the daily window should be closed")
	}

	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		_ = json.NewEncoder(w).Encode(ghRelease{TagName: "v1.0.0"})
	}))
	defer srv.Close()

	if got := update(srv.URL, "v1.0.0", false, discard); got.Outcome != OutcomeCurrent {
		t.Errorf("Outcome = %v, want OutcomeCurrent", got.Outcome)
	}
	if !asked {
		t.Error("update did not reach the server")
	}
}

func TestAdvice(t *testing.T) {
	cases := []struct {
		name string
		r    Result
		want string
	}{
		{"failed", Result{Outcome: OutcomeFailed}, ""},
		{"current", Result{Outcome: OutcomeCurrent, Tag: "v1.0.0"}, "this is already the newest release, so a newer build has to be published first"},
		{"installed", Result{Outcome: OutcomeInstalled, Tag: "v2.0.0"}, "updated to v2.0.0"},
		{"disabled", Result{Outcome: OutcomeDisabled, Tag: "v2.0.0"}, "v2.0.0 is available; restart without --no-update to install it"},
		{"manual", Result{Outcome: OutcomeManual, Tag: "v2.0.0"}, "v2.0.0 is available, but this install cannot replace itself; reinstall to update"},
	}
	for _, tc := range cases {
		if got := tc.r.Advice(); got != tc.want {
			t.Errorf("%s: Advice = %q, want %q", tc.name, got, tc.want)
		}
	}
}
