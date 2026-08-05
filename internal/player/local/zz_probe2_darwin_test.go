//go:build darwin && cgo

// SCAFFOLDING ONLY (not proposed code) — follow-up probes.
package local

import (
	"testing"
	"time"

	"github.com/simone-vibes/vibez/internal/provider"
)

// Is Seek broken generally, or only after EOS? Seek at t=2s, mid-playback, no EOS involved.
func TestProbeSeekMidPlayback(t *testing.T) {
	dir := musicDir(t)
	p := newQuiet(t, []provider.Track{track(dir, "one.mp3", "SOLO")})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	time.Sleep(2 * time.Second)
	_, posBefore, _, _ := state(t, p)
	t.Logf("t=2.0s (before seek):     pos=%v", posBefore)

	_ = p.Seek(1 * time.Second)
	for i := 1; i <= 4; i++ {
		time.Sleep(700 * time.Millisecond)
		_, pos, _, playing := state(t, p)
		t.Logf("t=+%.1fs after Seek(1s): pos=%-14v playing=%v", float64(i)*0.7, pos, playing)
	}
	_, posEnd, _, playingEnd := state(t, p)
	if posEnd <= 1100*time.Millisecond {
		t.Logf("RESULT: Seek KILLS playback outright (pos stuck at %v, but Playing=%v — UI would lie)", posEnd, playingEnd)
	} else {
		t.Logf("RESULT: Seek recovered, pos advanced to %v", posEnd)
	}
	_ = p.Close()
}

// Does a format ExtAudioFile can't decode ever reach EOS, or spin forever on
// unchecked ExtAudioFileRead errors? four.ogg reported duration=0.
func TestProbeOggNeverEnds(t *testing.T) {
	dir := musicDir(t)
	p := newQuiet(t, []provider.Track{
		track(dir, "four.ogg", "OGG"),
		track(dir, "one.mp3", "NEXT-MP3"),
	})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	start := time.Now()
	for i := 0; i < 16; i++ {
		time.Sleep(750 * time.Millisecond)
		title, pos, dur, playing := state(t, p)
		t.Logf("t=%4.1fs track=%-9s pos=%-14v dur=%-6v playing=%v", time.Since(start).Seconds(), title, pos, dur, playing)
		if title != "OGG" {
			t.Logf("RESULT: advanced off the ogg after %.1fs", time.Since(start).Seconds())
			_ = p.Close()
			return
		}
	}
	t.Logf("RESULT: still on the 5s ogg after %.1fs — never reaches EOS (unchecked ExtAudioFileRead error)", time.Since(start).Seconds())
	_ = p.Close()
}

// Single-track queue: does it loop forever regardless of repeat mode?
func TestProbeSingleTrackLoops(t *testing.T) {
	dir := musicDir(t)
	p := newQuiet(t, []provider.Track{track(dir, "one.mp3", "SOLO")})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)
	_ = p.SetRepeat(0) // RepeatModeOff — should stop at the end

	start := time.Now()
	var restarts int
	last := 10 * time.Second
	for i := 0; i < 16; i++ {
		time.Sleep(750 * time.Millisecond)
		_, pos, _, playing := state(t, p)
		if pos < last {
			restarts++
			t.Logf("t=%4.1fs pos WRAPPED to %v (restart #%d) playing=%v", time.Since(start).Seconds(), pos, restarts, playing)
		}
		last = pos
	}
	if restarts > 0 {
		t.Logf("RESULT: with RepeatMode=Off, the track restarted %d time(s) — Next() wraps via %% len(queue), SetRepeat is stored but never consulted", restarts)
	} else {
		t.Logf("RESULT: stopped at end as expected")
	}
	_ = p.Close()
}
