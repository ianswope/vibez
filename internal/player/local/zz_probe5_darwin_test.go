//go:build darwin && cgo

// SCAFFOLDING ONLY (not proposed code) — isolates the Close()-vs-EOS-start
// use-after-free without depending on the undecodable .ogg. A short valid track
// puts eosLoop inside playTrack's C.vibez_start (player_darwin.go:327) at the
// moment Close() frees the same object under p.mu (player_darwin.go:594).
// PROBE_CLOSE_DELAY_MS sweeps where Close() lands relative to the boundary.
// Run alone: the fault is unrecoverable and takes the process down.
package local

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/simone-vibes/vibez/internal/provider"
)

func TestProbeCloseAtEOSStart(t *testing.T) {
	dir := musicDir(t)

	delay := 420 * time.Millisecond
	if v := os.Getenv("PROBE_CLOSE_DELAY_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("PROBE_CLOSE_DELAY_MS=%q: %v", v, err)
		}
		delay = time.Duration(ms) * time.Millisecond
	}

	// short.mp3 is ~0.4s, so EOS — and the eosLoop's Next() -> playTrack ->
	// C.vibez_start for track B — lands a few hundred ms after playback starts.
	p := newQuiet(t, []provider.Track{track(dir, "short.mp3", "SHORT-A"), track(dir, "two.flac", "B")})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	time.Sleep(delay)
	title, pos, _, playing := state(t, p)
	t.Logf("Close() at t=%v (track=%q pos=%v playing=%v)", delay, title, pos, playing)
	if err := p.Close(); err != nil {
		t.Logf("Close returned err=%v", err)
	}
	time.Sleep(1500 * time.Millisecond) // let a late callback land
	t.Logf("RESULT: survived Close() at t=%v", delay)
}

// Discriminator: is the .ogg crash a narrow Close-vs-start race, or does the
// ogg leave state that any later Close() trips over? PROBE_CLOSE_DELAY_MS
// controls how long after Play() the Close() lands.
func TestProbeCloseAfterOggAdvance(t *testing.T) {
	dir := musicDir(t)

	delay := 900 * time.Millisecond
	if v := os.Getenv("PROBE_CLOSE_DELAY_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("PROBE_CLOSE_DELAY_MS=%q: %v", v, err)
		}
		delay = time.Duration(ms) * time.Millisecond
	}

	p := newQuiet(t, []provider.Track{track(dir, "four.ogg", "OGG-A"), track(dir, "one.mp3", "NEXT-MP3")})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	time.Sleep(delay)
	title, pos, dur, playing := state(t, p)
	t.Logf("Close() at t=%v (track=%q pos=%v dur=%v playing=%v)", delay, title, pos, dur, playing)
	if err := p.Close(); err != nil {
		t.Logf("Close returned err=%v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	t.Logf("RESULT: survived Close() at t=%v after the ogg", delay)
}

// Control: the same ogg -> mp3 advance with no Close() at all.
func TestProbeOggNoClose(t *testing.T) {
	dir := musicDir(t)
	p := newQuiet(t, []provider.Track{track(dir, "four.ogg", "OGG-A"), track(dir, "one.mp3", "NEXT-MP3")})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	for i := 0; i < 6; i++ {
		time.Sleep(500 * time.Millisecond)
		title, pos, dur, playing := state(t, p)
		t.Logf("t=%.1fs track=%-9s pos=%-15v dur=%-13v playing=%v", float64(i+1)*0.5, title, pos, dur, playing)
	}
	t.Log("RESULT: survived the ogg advance with no Close()")
}

// Mirrors TestProbeOggNeverEnds' structure — poll, then Close() the instant an
// EOS-driven track change is observable — but with two ordinary decodable
// tracks. PROBE_FIRST/PROBE_SECOND name them; a tight poll lands Close() inside
// playTrack's post-unlock window (player_darwin.go:327) rather than after it.
func TestProbeCloseOnTrackChange(t *testing.T) {
	dir := musicDir(t)

	first, second := "short.mp3", "two.flac"
	if v := os.Getenv("PROBE_FIRST"); v != "" {
		first = v
	}
	if v := os.Getenv("PROBE_SECOND"); v != "" {
		second = v
	}

	p := newQuiet(t, []provider.Track{track(dir, first, "A"), track(dir, second, "B")})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)
	mustStartPlaying(t, p)

	start := time.Now()
	for time.Since(start) < 12*time.Second {
		time.Sleep(5 * time.Millisecond)
		title, pos, _, playing := state(t, p)
		if title != "A" {
			t.Logf("track change seen at t=%v (track=%q pos=%v playing=%v) — calling Close() now",
				time.Since(start).Round(time.Millisecond), title, pos, playing)
			_ = p.Close()
			time.Sleep(1500 * time.Millisecond)
			t.Log("RESULT: survived Close() on the track change")
			return
		}
	}
	t.Fatal("no track change observed")
}
