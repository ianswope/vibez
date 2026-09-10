//go:build linux

package cdp

import (
	"os"
	"path/filepath"
	"testing"
)

// Helpers for the system-browser path, shared by the arm64 tests (where it is
// the only path) and the amd64 override tests. They drive discovery through
// VIBEZ_CHROME_PATH and an emptied widevineSystemDirs list so nothing depends
// on what is installed on the host.

// fakeBrowser writes an executable stub and points VIBEZ_CHROME_PATH at it,
// isolating HOME and the fixed CDM search list so discovery is hermetic.
func fakeBrowser(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "chromium")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fake browser: %v", err)
	}
	t.Setenv("VIBEZ_CHROME_PATH", bin)
	t.Setenv("CHROME_PATH", "")
	t.Setenv("HOME", dir)

	saved := widevineSystemDirs
	widevineSystemDirs = nil // only browser-adjacent discovery is exercised
	t.Cleanup(func() { widevineSystemDirs = saved })
	return bin
}

// installAdjacentCDM drops a fake Widevine CDM for this arch next to the given
// browser and returns the WidevineCdm directory.
func installAdjacentCDM(t *testing.T, browser string) string {
	t.Helper()
	cdmDir := filepath.Join(filepath.Dir(browser), "WidevineCdm")
	soDir := filepath.Join(cdmDir, "_platform_specific", widevinePlatformDir())
	if err := os.MkdirAll(soDir, 0o750); err != nil {
		t.Fatalf("mkdir cdm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(soDir, "libwidevinecdm.so"), []byte("fake cdm"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write cdm: %v", err)
	}
	return cdmDir
}
