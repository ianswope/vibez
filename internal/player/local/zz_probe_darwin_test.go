//go:build darwin && cgo

// SCAFFOLDING ONLY (not proposed code) — headless probes for the CoreAudio
// backend, driving the Player API directly so the runtime matrix can be
// exercised without the TUI. Volume is forced to 0 to stay quiet.
package local

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simone-vibes/vibez/internal/provider"
)

// musicDir resolves the corpus directory and refuses one it cannot read.
//
// PROBE_MUSIC_DIR must be absolute, because `go test` runs each test binary in
// its own package directory: a relative ./probe-corpus resolves against
// internal/player/local/, not the repo root. The old failure was silent rather
// than loud. Track IDs became paths to nothing, LoadTracks yielded an empty
// queue, SetPlaylist returned nil at its len(p.queue)==0 guard
// (player_darwin.go:473), and every `title != "A"` guard then fired at t~=6ms
// against track="" playing=false -- so a probe reported "survived" without ever
// reaching the window it exists to test.
func musicDir(t *testing.T) string {
	d := os.Getenv("PROBE_MUSIC_DIR")
	if d == "" {
		t.Skip("PROBE_MUSIC_DIR not set")
	}
	abs, err := filepath.Abs(d)
	if err != nil {
		t.Fatalf("PROBE_MUSIC_DIR=%q: %v", d, err)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		t.Fatalf("PROBE_MUSIC_DIR=%q resolved to %q, which is not a readable directory: %v", d, abs, err)
	}
	if abs != d {
		t.Logf("PROBE_MUSIC_DIR=%q resolved to %q (track IDs must be absolute)", d, abs)
	}
	return abs
}

// mustPlay fails the test unless want is playing within 2s. Every probe that
// races something against an in-flight track needs this: without it a probe
// whose playback never started still satisfies a `title != want` guard and
// reports a false survival.
func mustPlay(t *testing.T, p *Player, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if title, _, _, playing := state(t, p); title == want && playing {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	title, pos, _, playing := state(t, p)
	t.Fatalf("track %q never started (track=%q pos=%v playing=%v): the probe would not have reached its window",
		want, title, pos, playing)
}

// mustStartPlaying is mustPlay for probes whose first track is short enough to
// finish before an assertion on its title can land (short.mp3 is 0.4s). It only
// requires that playback started at all, which is what the false-survival bug
// turned on.
func mustStartPlaying(t *testing.T, p *Player) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if title, _, _, playing := state(t, p); title != "" && playing {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	title, pos, _, playing := state(t, p)
	t.Fatalf("playback never started (track=%q pos=%v playing=%v): the probe would not have reached its window",
		title, pos, playing)
}

func track(dir, name, title string) provider.Track {
	return provider.Track{ID: "local:" + filepath.Join(dir, name), Title: title, Artist: "probe"}
}

func newQuiet(t *testing.T, tracks []provider.Track) *Player {
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.LoadTracks(tracks)
	return p
}

func state(t *testing.T, p *Player) (title string, pos time.Duration, dur time.Duration, playing bool) {
	s, err := p.GetState()
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if s.Track != nil {
		title, dur = s.Track.Title, s.Track.Duration
	}
	return title, s.Position, dur, s.Playing
}

// Probe 1+2: duration reporting at 44.1kHz vs 48kHz, and whether position advances.
func TestProbeDurationAndPosition(t *testing.T) {
	dir := musicDir(t)
	for _, tc := range []struct {
		file, label string
		rate        int
	}{
		{"one.mp3", "44.1kHz mp3", 44100},
		{"two.flac", "44.1kHz flac", 44100},
		{"three.m4a", "44.1kHz m4a", 44100},
		{"four.ogg", "44.1kHz ogg", 44100},
		{"six_48k.flac", "48kHz flac", 48000},
	} {
		p := newQuiet(t, []provider.Track{track(dir, tc.file, tc.label)})
		_ = p.SetPlaylist("", 0)
		_ = p.SetVolume(0)

		_, _, dur, playing := state(t, p)
		time.Sleep(1500 * time.Millisecond)
		_, pos2, _, _ := state(t, p)

		t.Logf("%-14s file=%-13s duration=%-12v (want ~5s)  position@1.5s=%-12v  playing=%v",
			tc.label, tc.file, dur, pos2, playing)
		if tc.rate != 44100 {
			t.Logf("   ^ 48kHz: 5s*48000/44100 = 5.44s would confirm the hardcoded-44100 divisor")
		}
		_ = p.Close()
	}
}

// Probe 3: EOS auto-advance — the callback path nobody has seen run.
func TestProbeEOSAutoAdvance(t *testing.T) {
	dir := musicDir(t)
	p := newQuiet(t, []provider.Track{
		track(dir, "one.mp3", "TRACK-A"),
		track(dir, "two.flac", "TRACK-B"),
	})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	title0, _, _, _ := state(t, p)
	t.Logf("t=0.0s  track=%s", title0)

	deadline := time.Now().Add(12 * time.Second)
	start := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		title, pos, _, playing := state(t, p)
		t.Logf("t=%.1fs  track=%-8s pos=%-12v playing=%v", time.Since(start).Seconds(), title, pos, playing)
		if title != title0 {
			t.Logf("RESULT: EOS auto-advance FIRED after %.1fs (%s -> %s)", time.Since(start).Seconds(), title0, title)
			_ = p.Close()
			return
		}
	}
	t.Errorf("RESULT: EOS auto-advance NEVER FIRED within 12s for a 5s track")
	_ = p.Close()
}

// Probe 4: seek backwards after a track has fully ended (suspect: C done flag never resets).
func TestProbeSeekAfterEOS(t *testing.T) {
	dir := musicDir(t)
	p := newQuiet(t, []provider.Track{track(dir, "one.mp3", "SOLO")})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	time.Sleep(7 * time.Second) // let it run past the 5s end
	_, posEnd, _, playingEnd := state(t, p)
	t.Logf("after 7s on a 5s track: pos=%v playing=%v", posEnd, playingEnd)

	_ = p.Seek(1 * time.Second)
	time.Sleep(1500 * time.Millisecond)
	_, posAfter, _, playingAfter := state(t, p)
	t.Logf("after Seek(1s)+1.5s wait: pos=%v playing=%v", posAfter, playingAfter)
	if posAfter <= 1100*time.Millisecond {
		t.Logf("RESULT: position did NOT advance past the seek target — playback appears dead after EOS")
	} else {
		t.Logf("RESULT: playback resumed after seek (pos advanced to %v)", posAfter)
	}
	_ = p.Close()
}

// Probe 5+6: ClearQueue then try to play; and whether SetQueue honours its id list.
func TestProbeQueueSemantics(t *testing.T) {
	dir := musicDir(t)
	a := track(dir, "one.mp3", "TRACK-A")
	b := track(dir, "two.flac", "TRACK-B")
	c := track(dir, "three.m4a", "TRACK-C")

	t.Run("SetQueue honours id list", func(t *testing.T) {
		p := newQuiet(t, []provider.Track{a, b, c})
		_ = p.SetVolume(0)
		// Ask for exactly [C, B]: contract says queue is replaced and playback starts at C.
		_ = p.SetQueue([]string{c.ID, b.ID})
		title, _, _, _ := state(t, p)
		p.mu.RLock()
		qlen, idx := len(p.queue), p.idx
		p.mu.RUnlock()
		t.Logf("SetQueue([C,B]) -> now playing=%q queue len=%d idx=%d (want len=2 playing TRACK-C)", title, qlen, idx)
		if qlen != 2 {
			t.Logf("RESULT: queue was NOT replaced — SetQueue ignored its id list beyond ids[0]")
		}
		_ = p.Close()
	})

	t.Run("play after ClearQueue", func(t *testing.T) {
		p := newQuiet(t, []provider.Track{a, b})
		_ = p.SetPlaylist("", 0)
		_ = p.SetVolume(0)
		time.Sleep(500 * time.Millisecond)

		_ = p.ClearQueue()
		if err := p.SetQueue([]string{a.ID}); err != nil {
			t.Logf("SetQueue after clear returned err=%v", err)
		}
		_ = p.AppendQueue([]string{a.ID, b.ID})
		p.mu.RLock()
		qlen := len(p.queue)
		p.mu.RUnlock()
		title, _, _, playing := state(t, p)
		t.Logf("after ClearQueue + SetQueue([A]) + AppendQueue([A,B]): queue len=%d track=%q playing=%v", qlen, title, playing)
		if qlen == 0 || !playing {
			t.Logf("RESULT: queue is DEAD after ClearQueue — AppendQueue resolves ids against the (now empty) queue")
		}
		_ = p.Close()
	})
}

// Probe 7: Close() while playing — EOS callback vs deleted cgo.Handle.
// Run alone: a panic on a cgo callback thread is unrecoverable and kills the process.
func TestProbeCloseWhilePlaying(t *testing.T) {
	dir := musicDir(t)
	p := newQuiet(t, []provider.Track{track(dir, "one.mp3", "A"), track(dir, "two.flac", "B")})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	// Close right around the natural end of the track, racing the EOS callback.
	time.Sleep(4900 * time.Millisecond)
	t.Log("calling Close() at t=4.9s, ~100ms before EOS")
	if err := p.Close(); err != nil {
		t.Logf("Close returned err=%v", err)
	}
	time.Sleep(2 * time.Second) // give a late callback time to land on a deleted handle
	t.Log("RESULT: survived Close-during-EOS without a panic")
}
