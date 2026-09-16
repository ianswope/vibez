package local_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simone-vibes/vibez/internal/provider/local"
)

// noticeDir creates a temp directory holding the named empty files and returns
// a Provider scanning it, plus the directory.
func noticeDir(t *testing.T, names ...string) (*local.Provider, string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte{}, 0o600); err != nil {
			t.Fatalf("creating %s: %v", n, err)
		}
	}
	p, err := local.New(dir)
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	return p, dir
}

func TestScanNotice_SilentWhenEverythingIndexed(t *testing.T) {
	p, _ := noticeDir(t, "a.mp3", "b.flac", "c.m4a")
	if got := p.ScanNotice(); got != "" {
		t.Errorf("ScanNotice() = %q, want empty when nothing was left out", got)
	}
}

func TestScanNotice_EmptyDirectory(t *testing.T) {
	p, dir := noticeDir(t)
	want := "no playable tracks in " + dir + ": the directory is empty"
	if got := p.ScanNotice(); got != want {
		t.Errorf("ScanNotice() = %q, want %q", got, want)
	}
}

func TestScanNotice_NothingPlayableNamesTheSupportedSet(t *testing.T) {
	p, dir := noticeDir(t, "one.txt", "two.txt", "three.txt", "sleeve.pdf")
	want := "no playable tracks in " + dir + ": " + wantPlatformName + " plays " + wantSupportedExts +
		"; skipped 4 files (3 .txt, 1 .pdf)"
	if got := p.ScanNotice(); got != want {
		t.Errorf("ScanNotice() = %q, want %q", got, want)
	}
}

func TestScanNotice_ReportsSkipsAlongsideIndexedTracks(t *testing.T) {
	p, dir := noticeDir(t, "song.mp3", "cover.txt", "notes.txt")
	want := "indexed 1 of 3 files in " + dir + ": " + wantPlatformName + " plays " + wantSupportedExts +
		"; skipped 2 files (2 .txt)"
	if got := p.ScanNotice(); got != want {
		t.Errorf("ScanNotice() = %q, want %q", got, want)
	}
}

func TestScanNotice_FileWithNoExtension(t *testing.T) {
	p, dir := noticeDir(t, "README")
	want := "no playable tracks in " + dir + ": " + wantPlatformName + " plays " + wantSupportedExts +
		"; skipped 1 file (1 no extension)"
	if got := p.ScanNotice(); got != want {
		t.Errorf("ScanNotice() = %q, want %q", got, want)
	}
}

// A music directory can hold a long tail of odd extensions. The list names the
// three commonest and counts the rest, so the line stays inside a status bar.
func TestScanNotice_CapsTheExtensionList(t *testing.T) {
	p, _ := noticeDir(t, "a.aa", "b.bb", "c.cc", "d.dd", "e.ee")
	got := p.ScanNotice()
	if !strings.Contains(got, "skipped 5 files (1 .aa, 1 .bb, 1 .cc, +2 more)") {
		t.Errorf("ScanNotice() = %q, want the list capped at three extensions", got)
	}
}

// A file that will not open is counted separately: nothing about the format
// explains it, so the message must not blame the platform.
func TestScanNotice_UnreadableFilesAreNotBlamedOnTheFormat(t *testing.T) {
	_, dir := noticeDir(t, "song.mp3")
	if err := os.Symlink(filepath.Join(dir, "gone.mp3"), filepath.Join(dir, "broken.mp3")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	p, err := local.New(dir)
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	want := "indexed 1 of 2 files in " + dir + ": 1 file could not be read"
	if got := p.ScanNotice(); got != want {
		t.Errorf("ScanNotice() = %q, want %q", got, want)
	}
}

// OGG plays on Linux through GStreamer and not on macOS through CoreAudio.
// That split is the case the notice exists for.
func TestScanNotice_OggFollowsThePlatformSplit(t *testing.T) {
	p, dir := noticeDir(t, "track.ogg")
	got := p.ScanNotice()
	if oggPlayable {
		if got != "" {
			t.Errorf("ScanNotice() = %q, want empty where OGG plays", got)
		}
		return
	}
	want := "no playable tracks in " + dir + ": " + wantPlatformName + " plays " + wantSupportedExts +
		"; skipped 1 file (1 .ogg)"
	if got != want {
		t.Errorf("ScanNotice() = %q, want %q", got, want)
	}
}

// The path is the part of the line the user already knows, so it gives up its
// room to the part they do not.
func TestScanNotice_ElidesTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Music")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sleeve.pdf"), []byte{}, 0o600); err != nil {
		t.Fatalf("creating file: %v", err)
	}

	p, err := local.New(dir)
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	if got := p.ScanNotice(); !strings.HasPrefix(got, "no playable tracks in ~/Music: ") {
		t.Errorf("ScanNotice() = %q, want the home directory elided to ~", got)
	}
}
