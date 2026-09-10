//go:build linux && arm64

package cdp

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The arm64 backend discovers a system Chromium/Chrome and a system-registered
// Widevine CDM instead of downloading Google Chrome (which Google does not
// publish for Linux/arm64). Tests drive discovery through the
// VIBEZ_CHROME_PATH override and an overridable widevineSystemDirs list so they
// never depend on what is actually installed on the host; the helpers live in
// browser_system_test.go, shared with the amd64 override tests.

func TestFindSystemBrowser_EnvOverride(t *testing.T) {
	bin := fakeBrowser(t)
	got, err := findSystemBrowser()
	if err != nil {
		t.Fatalf("findSystemBrowser: %v", err)
	}
	if got != bin {
		t.Errorf("findSystemBrowser() = %q, want %q", got, bin)
	}
}

func TestFindSystemBrowser_EnvOverrideInvalid(t *testing.T) {
	t.Setenv("VIBEZ_CHROME_PATH", filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := findSystemBrowser(); err == nil {
		t.Error("expected error for non-existent VIBEZ_CHROME_PATH, got nil")
	}
}

func TestChromePath_UsesSystemBrowser(t *testing.T) {
	bin := fakeBrowser(t)
	if got := ChromePath(); got != bin {
		t.Errorf("ChromePath() = %q, want %q", got, bin)
	}
}

func TestHelperPath_EqualsChromePath(t *testing.T) {
	fakeBrowser(t)
	if HelperPath() != ChromePath() {
		t.Errorf("HelperPath() = %q, want == ChromePath() = %q", HelperPath(), ChromePath())
	}
}

func TestLinkHelper_NoOpOnARM64(t *testing.T) {
	bin := fakeBrowser(t)
	linkHelper() // must not panic and must not create a sibling helper
	if _, err := os.Stat(filepath.Join(filepath.Dir(bin), "vibez-helper")); err == nil {
		t.Error("linkHelper() created a helper link on arm64; expected no-op")
	}
}

func TestWidevineCDMDir_DiscoversAdjacentCDM(t *testing.T) {
	bin := fakeBrowser(t)
	if got := widevineCDMDir(); got != "" {
		t.Fatalf("widevineCDMDir() = %q before install, want empty", got)
	}
	want := installAdjacentCDM(t, bin)
	if got := widevineCDMDir(); got != want {
		t.Errorf("widevineCDMDir() = %q, want %q", got, want)
	}
}

func TestAvailable_RequiresBrowserAndCDM(t *testing.T) {
	bin := fakeBrowser(t)
	if Available() {
		t.Error("Available() = true with no Widevine CDM; want false")
	}
	installAdjacentCDM(t, bin)
	if !Available() {
		t.Error("Available() = false with browser + CDM present; want true")
	}
}

func TestChromeLaunchArgs_ARM64WidevinePath(t *testing.T) {
	bin := fakeBrowser(t)
	cdmDir := installAdjacentCDM(t, bin)
	args := chromeLaunchArgs(true, false)
	if !slices.Contains(args, "--widevine-path="+cdmDir) {
		t.Errorf("chromeLaunchArgs missing --widevine-path=%s; got %v", cdmDir, args)
	}
	if !slices.Contains(args, "--headless=new") {
		t.Error("chromeLaunchArgs should contain --headless=new when headless=true")
	}
}
