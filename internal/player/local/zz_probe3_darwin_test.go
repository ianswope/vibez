//go:build darwin && cgo

// SCAFFOLDING ONLY (not proposed code) — cgo.Handle churn stress.
package local

import (
	"testing"
	"time"

	"github.com/simone-vibes/vibez/internal/provider"
)

// playTrack does handle.Delete() + cgo.NewHandle() OUTSIDE the mutex while an
// EOS callback may be in flight on a CoreAudio thread. Churn transitions hard
// and see whether Value() ever lands on a deleted handle.
func TestProbeHandleChurn(t *testing.T) {
	dir := musicDir(t)
	p := newQuiet(t, []provider.Track{
		track(dir, "one.mp3", "A"),
		track(dir, "two.flac", "B"),
		track(dir, "three.m4a", "C"),
		track(dir, "six_48k.flac", "D"),
	})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	for i := 0; i < 300; i++ {
		_ = p.Next()
		if i%3 == 0 {
			_ = p.Previous()
		}
		time.Sleep(12 * time.Millisecond)
	}
	title, _, _, playing := state(t, p)
	t.Logf("RESULT: survived 300 rapid transitions; track=%q playing=%v", title, playing)
	_ = p.Close()
	time.Sleep(500 * time.Millisecond)
}

// Same churn, but let tracks reach natural EOS between transitions so real
// callbacks race the handle swap.
func TestProbeEOSRaceChurn(t *testing.T) {
	dir := musicDir(t)
	p := newQuiet(t, []provider.Track{
		track(dir, "one.mp3", "A"),
		track(dir, "two.flac", "B"),
	})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	// 5s tracks; poke Next near each natural boundary to collide with EOS.
	for i := 0; i < 6; i++ {
		time.Sleep(4950 * time.Millisecond)
		_ = p.Next()
		t.Logf("poked Next() at boundary %d", i+1)
	}
	t.Log("RESULT: survived 6 EOS-boundary collisions without a panic")
	_ = p.Close()
}
