//go:build linux && amd64

package cdp

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	playwright "github.com/mxschmitt/playwright-go"
)

// These tests cover the amd64 layout, where vibez downloads Google Chrome into
// its private cache and hard-links a vibez-helper alias, and the override that
// swaps in a system browser instead. The arm64 backend always uses a system
// browser (see browser_arm64_test.go).

// noBrowserOverride clears VIBEZ_CHROME_PATH and CHROME_PATH so the bundled
// layout is what gets tested, whatever the developer's shell exports.
func noBrowserOverride(t *testing.T) {
	t.Helper()
	t.Setenv("VIBEZ_CHROME_PATH", "")
	t.Setenv("CHROME_PATH", "")
}

func bundledWidevineArg() string {
	return "--widevine-path=" + filepath.Join(chromeInstallDir(), "opt", "google", "chrome", "WidevineCdm")
}

func TestChromePath_IsAbsolute(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	noBrowserOverride(t)
	got := ChromePath()
	if !filepath.IsAbs(got) {
		t.Errorf("ChromePath() = %q, want absolute path", got)
	}
	if filepath.Base(got) != "chrome" {
		t.Errorf("ChromePath() base = %q, want %q", filepath.Base(got), "chrome")
	}
}

func TestHelperPath_IsAbsolute(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	noBrowserOverride(t)
	got := HelperPath()
	if !filepath.IsAbs(got) {
		t.Errorf("HelperPath() = %q, want absolute path", got)
	}
	if filepath.Base(got) != "vibez-helper" {
		t.Errorf("HelperPath() base = %q, want %q", filepath.Base(got), "vibez-helper")
	}
}

func TestLinkHelper_CreatesHardLink(t *testing.T) {
	// Set up a fake chrome directory structure in a temp cache dir.
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	noBrowserOverride(t)

	// Create the directories and a fake chrome binary.
	chromeBin := ChromePath()
	if err := os.MkdirAll(filepath.Dir(chromeBin), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(chromeBin, []byte("fake chrome"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write chrome: %v", err)
	}

	// Call linkHelper — should create vibez-helper.
	linkHelper()

	if _, err := os.Stat(HelperPath()); err != nil {
		t.Errorf("vibez-helper not created by linkHelper(): %v", err)
	}
}

func TestLinkHelper_IdempotentWhenHelperExists(t *testing.T) {
	// Set up a fake chrome + helper already present.
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	noBrowserOverride(t)

	chromeBin := ChromePath()
	if err := os.MkdirAll(filepath.Dir(chromeBin), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(chromeBin, []byte("fake chrome"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write chrome: %v", err)
	}
	if err := os.WriteFile(HelperPath(), []byte("fake helper"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write helper: %v", err)
	}

	// Should not panic and should be a no-op.
	linkHelper()
}

func TestEnsureBrowser_AlreadyInstalled(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	noBrowserOverride(t)

	chromeBin := ChromePath()
	if err := os.MkdirAll(filepath.Dir(chromeBin), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(chromeBin, []byte("fake chrome"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write chrome: %v", err)
	}

	// Mock the playwright driver as also already installed and up-to-date.
	driver, err := playwright.NewDriver(&playwright.RunOptions{
		DriverDirectory: driverDir(),
	})
	if err != nil {
		t.Fatalf("new driver: %v", err)
	}
	pkgDir := filepath.Join(driverDir(), "package")
	if err := os.MkdirAll(pkgDir, 0o750); err != nil {
		t.Fatalf("mkdir driver package: %v", err)
	}
	pkgJSON := fmt.Sprintf(`{"version": %q}`, driver.Version)
	if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(pkgJSON), 0o600); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write driver package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "cli.js"), []byte(driver.Version), 0o600); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write driver CLI: %v", err)
	}
	setVersionReportingNode(t)

	var progress []string
	err = EnsureBrowser(func(s string) { progress = append(progress, s) })
	if err != nil {
		t.Errorf("EnsureBrowser when already installed should return nil, got: %v", err)
	}
	// No download should have been triggered.
	if len(progress) > 0 {
		t.Errorf("EnsureBrowser when installed should not call onProgress, got: %v", progress)
	}
}

func TestChromePath_HonoursOverride(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	bin := fakeBrowser(t)
	if got := ChromePath(); got != bin {
		t.Errorf("ChromePath() = %q, want the override %q", got, bin)
	}
	if got := HelperPath(); got != bin {
		t.Errorf("HelperPath() = %q, want == override %q", got, bin)
	}
	linkHelper()
	if _, err := os.Stat(filepath.Join(filepath.Dir(bin), "vibez-helper")); err == nil {
		t.Error("linkHelper() hard-linked next to the override; expected no-op")
	}
	if _, err := os.Stat(chromeInstallDir()); err == nil {
		t.Error("linkHelper() created the private Chrome dir for an override; expected no-op")
	}
}

func TestEnsureBrowser_InvalidOverrideFailsBeforeDownload(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("VIBEZ_CHROME_PATH", filepath.Join(t.TempDir(), "does-not-exist"))
	var progress []string
	err := EnsureBrowser(func(s string) { progress = append(progress, s) })
	if err == nil {
		t.Fatal("EnsureBrowser with an unusable VIBEZ_CHROME_PATH returned nil; want error")
	}
	if len(progress) > 0 {
		t.Errorf("EnsureBrowser reported progress %v before failing on the override; want none", progress)
	}
}

func TestWidevineCDMDir_DiscoversX64CDM(t *testing.T) {
	bin := fakeBrowser(t)
	if got := widevineCDMDir(); got != "" {
		t.Fatalf("widevineCDMDir() = %q before install, want empty", got)
	}
	want := installAdjacentCDM(t, bin)
	if got := widevineCDMDir(); got != want {
		t.Errorf("widevineCDMDir() = %q, want %q", got, want)
	}
}

func TestChromeLaunchArgs_OverrideUsesSystemCDM(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	bin := fakeBrowser(t)
	cdmDir := installAdjacentCDM(t, bin)
	args := chromeLaunchArgs(true, false)
	if !slices.Contains(args, "--widevine-path="+cdmDir) {
		t.Errorf("chromeLaunchArgs missing --widevine-path=%s; got %v", cdmDir, args)
	}
	if slices.Contains(args, bundledWidevineArg()) {
		t.Errorf("chromeLaunchArgs still passes the bundled CDM with an override set; got %v", args)
	}
}

func TestChromeLaunchArgs_BundledCDMByDefault(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	noBrowserOverride(t)
	if args := chromeLaunchArgs(true, false); !slices.Contains(args, bundledWidevineArg()) {
		t.Errorf("chromeLaunchArgs missing %s; got %v", bundledWidevineArg(), args)
	}
}

// The override makes the system-browser path reachable on amd64, where the
// arm64 guidance is wrong twice over: it names a distro that is not this one,
// and it suggests setting the variable the user has already set.
func TestSystemBrowserHelp_OverrideAdviceSuitsAMD64(t *testing.T) {
	fakeBrowser(t)
	got := systemBrowserHelp()
	if !strings.Contains(got, "VIBEZ_CHROME_PATH") {
		t.Errorf("systemBrowserHelp() = %q, want it to name the override in force", got)
	}
	if !strings.Contains(got, "unset") {
		t.Errorf("systemBrowserHelp() = %q, want it to offer unsetting the override", got)
	}
	if strings.Contains(got, "Arch Linux ARM") {
		t.Errorf("systemBrowserHelp() = %q, want no arm64 install guidance on amd64", got)
	}
}

func TestSystemBrowserHelp_WithoutOverrideAsksForAnInstall(t *testing.T) {
	noBrowserOverride(t)
	got := systemBrowserHelp()
	if !strings.Contains(got, "install Chromium") {
		t.Errorf("systemBrowserHelp() = %q, want install guidance when no override is set", got)
	}
	if strings.Contains(got, "unset") {
		t.Errorf("systemBrowserHelp() = %q, want no unset advice when no override is set", got)
	}
}

func TestEnsureBrowser_MissingCDMBlamesTheOverride(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fakeBrowser(t) // usable browser, no CDM anywhere it looks
	err := EnsureBrowser(func(string) {})
	if err == nil {
		t.Fatal("EnsureBrowser with an override and no Widevine CDM returned nil; want error")
	}
	if !strings.Contains(err.Error(), "VIBEZ_CHROME_PATH") {
		t.Errorf("EnsureBrowser error = %q, want it to name the override in force", err)
	}
	if strings.Contains(err.Error(), "Arch Linux ARM") {
		t.Errorf("EnsureBrowser error = %q, want no arm64 install guidance on amd64", err)
	}
}
