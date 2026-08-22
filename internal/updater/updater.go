package updater

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	repo          = "simonepelosi/vibez"
	apiURL        = "https://api.github.com/repos/" + repo + "/releases/latest"
	checkInterval = 24 * time.Hour
	apiTimeout    = 5 * time.Second
	dlTimeout     = 2 * time.Minute
	// maxBinarySize caps extraction to guard against decompression bombs.
	maxBinarySize = 256 << 20 // 256 MB
)

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Outcome says what an update attempt did.
type Outcome int

const (
	// OutcomeFailed means the check itself did not complete, so nothing is
	// known about whether a newer release exists.
	OutcomeFailed Outcome = iota
	// OutcomeCurrent means this build is already the newest release.
	OutcomeCurrent
	// OutcomeInstalled means a newer release was installed and the caller
	// should re-exec Exe.
	OutcomeInstalled
	// OutcomeDisabled means a newer release exists and was left alone because
	// updates were turned off.
	OutcomeDisabled
	// OutcomeManual means a newer release exists but this process did not
	// install it: no asset for the platform, a binary it cannot replace, or a
	// download that failed. Either way the user has to update it themselves.
	OutcomeManual
)

// Result reports what an update attempt did, so a caller that knows the build
// is broken can explain the situation rather than repeating "update vibez".
type Result struct {
	Outcome Outcome
	// Tag is the newer release, when the check got far enough to learn it.
	Tag string
	// Exe is the binary to re-exec, set only for OutcomeInstalled.
	Exe string
}

// Advice is a one-clause summary of what can be done about the update state.
// It is empty when the check failed, since nothing is known to advise on.
func (r Result) Advice() string {
	switch r.Outcome {
	case OutcomeCurrent:
		return "this is already the newest release, so a newer build has to be published first"
	case OutcomeInstalled:
		return fmt.Sprintf("updated to %s", r.Tag)
	case OutcomeDisabled:
		return fmt.Sprintf("%s is available; restart without --no-update to install it", r.Tag)
	case OutcomeManual:
		return fmt.Sprintf("%s is available, but this install cannot replace itself; reinstall to update", r.Tag)
	default:
		return ""
	}
}

// executable is indirected so tests can point the self-replace at a temp file
// instead of the test binary.
var executable = os.Executable

// CheckAndUpdate checks GitHub for a newer release. If one is found it
// downloads, verifies the SHA-256 checksum, and installs it in-place.
// It returns the path of the updated binary when a restart is needed, or ""
// when already up to date, on error, or when noUpdate is true.
//
// The caller is responsible for re-execing after cleaning up (e.g. after the
// TUI exits). All errors are handled internally — the function never blocks
// startup fatally.
//
// It looks at most once every 24 hours. UpdateNow is for callers that cannot
// wait for the next window.
func CheckAndUpdate(current string, noUpdate bool, log func(string)) string {
	if noUpdate {
		return ""
	}
	if !shouldCheck() {
		return ""
	}
	return update(apiURL, current, true, log).Exe
}

// UpdateNow asks GitHub straight away, ignoring the once-a-day window, and
// reports what it found. It is for callers that already know this build cannot
// work: a rejected developer token is not something the user can wait out, and
// the throttle would otherwise leave them on a broken build for another day.
//
// With noUpdate set it still reports whether a newer release exists, and
// installs nothing.
func UpdateNow(current string, noUpdate bool, log func(string)) Result {
	return update(apiURL, current, !noUpdate, log)
}

// update is the testable core. api is the releases endpoint, and install says
// whether a newer release may replace this binary.
func update(api, current string, install bool, log func(string)) Result {
	log("Checking for updates…")

	rel, err := fetchLatestRelease(api)
	if err != nil {
		return Result{Outcome: OutcomeFailed}
	}

	markChecked()

	if !isNewer(rel.TagName, current) {
		return Result{Outcome: OutcomeCurrent, Tag: rel.TagName}
	}

	// Anything past here has a newer release to report, and reaching the end
	// is the only way to come back with something better than OutcomeManual.
	res := Result{Outcome: OutcomeManual, Tag: rel.TagName}

	assetName := fmt.Sprintf("vibez_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	var downloadURL, checksumURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case assetName:
			downloadURL = a.BrowserDownloadURL
		case "checksums.txt":
			checksumURL = a.BrowserDownloadURL
		}
	}
	// Checked before install, so a platform with no published asset - today,
	// linux/arm64 - is never told to drop --no-update for a build that is not
	// there.
	if downloadURL == "" {
		return res
	}
	if !install {
		res.Outcome = OutcomeDisabled
		return res
	}

	// Only attempt self-update for writable, self-managed installs.
	exe, err := executable()
	if err != nil {
		return res
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return res
	}
	if !isWritable(exe) {
		return res
	}

	log(fmt.Sprintf("Downloading update %s…", rel.TagName))

	tmpDir, err := os.MkdirTemp("", "vibez-update-*")
	if err != nil {
		return res
	}

	tarPath := filepath.Join(tmpDir, assetName)
	if err := downloadFile(downloadURL, tarPath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return res
	}

	if checksumURL != "" {
		if err := verifyChecksum(tarPath, assetName, checksumURL); err != nil {
			log("Update aborted: checksum verification failed")
			_ = os.RemoveAll(tmpDir)
			return res
		}
	}

	log("Installing update…")

	newBin := filepath.Join(tmpDir, "vibez")
	if err := extractBinary(tarPath, "vibez", newBin); err != nil {
		_ = os.RemoveAll(tmpDir)
		return res
	}
	if err := os.Chmod(newBin, 0o755); err != nil { //nolint:gosec // executables require 0755
		_ = os.RemoveAll(tmpDir)
		return res
	}

	// Atomic replace: write to exe.new, rename over exe.
	tmpBin := exe + ".new"
	data, err := os.ReadFile(newBin) //nolint:gosec // path comes from our own tmpDir
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return res
	}
	if err := os.WriteFile(tmpBin, data, 0o755); err != nil { //nolint:gosec // executable permissions required
		_ = os.RemoveAll(tmpDir)
		return res
	}
	if err := os.Rename(tmpBin, exe); err != nil {
		_ = os.Remove(tmpBin)
		_ = os.RemoveAll(tmpDir)
		return res
	}

	log(fmt.Sprintf("Updated to %s — restarting…", rel.TagName))
	_ = os.RemoveAll(tmpDir)
	return Result{Outcome: OutcomeInstalled, Tag: rel.TagName, Exe: exe}
}

func fetchLatestRelease(api string) (*ghRelease, error) {
	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Get(api) //nolint:gosec // URL is a hardcoded constant or test server
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API: unexpected status %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func downloadFile(url, dst string) error {
	client := &http.Client{Timeout: dlTimeout}
	resp, err := client.Get(url) //nolint:gosec // URL comes from the GitHub releases API response
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	f, err := os.Create(dst) //nolint:gosec // dst is a path inside our own tmpDir
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}

func verifyChecksum(tarPath, assetName, checksumURL string) error {
	client := &http.Client{Timeout: apiTimeout}
	resp, err := client.Get(checksumURL) //nolint:gosec // URL comes from the GitHub releases API response
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// checksums.txt format: "<sha256>  <filename>" per line.
	var expected string
	for line := range strings.SplitSeq(string(body), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == assetName {
			expected = parts[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum for %s not found in checksums.txt", assetName)
	}

	f, err := os.Open(tarPath) //nolint:gosec // tarPath is a path inside our own tmpDir
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if actual := hex.EncodeToString(h.Sum(nil)); actual != expected {
		return fmt.Errorf("checksum mismatch: got %s, want %s", actual, expected)
	}
	return nil
}

func extractBinary(tarPath, binaryName, dst string) error {
	f, err := os.Open(tarPath) //nolint:gosec // tarPath is a path inside our own tmpDir
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) == binaryName && hdr.Typeflag == tar.TypeReg {
			out, err := os.Create(dst) //nolint:gosec // dst is a path inside our own tmpDir
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, io.LimitReader(tr, maxBinarySize))
			if closeErr := out.Close(); closeErr != nil && copyErr == nil {
				copyErr = closeErr
			}
			return copyErr
		}
	}
	return fmt.Errorf("binary %q not found in archive", binaryName)
}

func isWritable(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY, 0) //nolint:gosec // intentional write-check
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// isNewer reports whether latestTag represents a higher version than currentTag.
// Both may carry a leading "v". Uses integer comparison per component so that
// e.g. 0.0.10 > 0.0.9 is handled correctly.
func isNewer(latestTag, currentTag string) bool {
	l := parseVersion(strings.TrimPrefix(latestTag, "v"))
	c := parseVersion(strings.TrimPrefix(currentTag, "v"))
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseVersion(v string) [3]int {
	parts := strings.SplitN(v, ".", 3)
	var r [3]int
	for i, p := range parts {
		if i >= 3 {
			break
		}
		r[i], _ = strconv.Atoi(p) //nolint:gosec // i is always < 3: SplitN cap + guard above
	}
	return r
}

func cacheDir() string {
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return filepath.Join(d, "vibez")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "vibez")
}

func shouldCheck() bool {
	stamp := filepath.Join(cacheDir(), "last_update_check")
	info, err := os.Stat(stamp)
	if err != nil {
		return true // file missing → check
	}
	return time.Since(info.ModTime()) > checkInterval
}

func markChecked() {
	dir := cacheDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	stamp := filepath.Join(dir, "last_update_check")
	f, err := os.OpenFile(stamp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // stamp path is derived from our own cacheDir()
	if err != nil {
		return
	}
	_ = f.Close()
}
