//go:build linux

package local_test

const (
	wantAudioFileCount = 4

	// Scan-notice expectations for this platform's supported set.
	wantPlatformName  = "Linux"
	wantSupportedExts = ".flac, .m4a, .mp3, .ogg"
	oggPlayable       = true
)
