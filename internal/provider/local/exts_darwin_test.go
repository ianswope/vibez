//go:build darwin

package local_test

const (
	wantAudioFileCount = 3

	// Scan-notice expectations for this platform's supported set.
	wantPlatformName  = "macOS"
	wantSupportedExts = ".flac, .m4a, .mp3"
	oggPlayable       = false
)
