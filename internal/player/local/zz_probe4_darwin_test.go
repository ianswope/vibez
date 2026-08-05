//go:build darwin && cgo

// SCAFFOLDING ONLY (not proposed code) — scan + odd-filename checks.
package local

import (
	"testing"
	"time"

	"github.com/simone-vibes/vibez/internal/provider"
)

func TestProbeHashFilename(t *testing.T) {
	dir := musicDir(t)
	p := newQuiet(t, []provider.Track{track(dir, "five#hash.mp3", "HASH")})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)
	time.Sleep(1500 * time.Millisecond)
	title, pos, dur, playing := state(t, p)
	s, _ := p.GetState()
	t.Logf("track=%q pos=%v dur=%v playing=%v err=%q", title, pos, dur, playing, s.Error)
	if pos == 0 || s.Error != "" {
		t.Logf("RESULT: '#' filename FAILED to play")
	} else {
		t.Logf("RESULT: '#' filename plays fine (raw POSIX path, no file:// URI)")
	}
	_ = p.Close()
}
