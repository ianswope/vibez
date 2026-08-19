//go:build darwin && cgo

// SCAFFOLDING ONLY (not proposed code). Covers the three Close/destroy windows
// that a34eae3's audioWg does not close. The WaitGroup counts eosLoop only, so
// it serialises the EOS-driven advance against Close and nothing else. Every
// probe here reaches C.vibez_start (player_darwin.go:330) from a goroutine the
// WaitGroup is blind to, or frees the object from a destroyer other than Close.
// Run each alone: the fault is unrecoverable and takes the process down.
package local

import (
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/simone-vibes/vibez/internal/player"
	"github.com/simone-vibes/vibez/internal/provider"
)

// closeDelay reads a probe's post-observation delay knob. 0 keeps Close() inside
// playTrack's post-unlock window; a delay past the window is the control.
func closeDelay(t *testing.T, env string, def time.Duration) time.Duration {
	v := os.Getenv(env)
	if v == "" {
		return def
	}
	ms, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("%s=%q: %v", env, v, err)
	}
	return time.Duration(ms) * time.Millisecond
}

// Window 1: user-initiated Next() vs Close(), no eosLoop involved.
//
// The TUI puts these on different goroutines by construction: "n" dispatches
// Next through playerCmd (internal/tui/model.go:2095) as a tea.Cmd, which
// bubbletea runs off the event loop, while Close is called synchronously in
// handleKey (:1162) and executeCommand (:1426) on the event loop itself. So
// this is "press n, then q" and audioWg does not count the Next goroutine.
//
// PROBE_NEXT_CLOSE_DELAY_MS delays the Close past the window as a control.
func TestProbeCloseDuringManualNext(t *testing.T) {
	dir := musicDir(t)
	delay := closeDelay(t, "PROBE_NEXT_CLOSE_DELAY_MS", 0)

	p := newQuiet(t, []provider.Track{
		track(dir, "one.mp3", "A"),
		track(dir, "two.flac", "B"),
	})
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)
	time.Sleep(500 * time.Millisecond) // let A settle so the change is unambiguous

	go func() { _ = p.Next() }() // the tea.Cmd goroutine

	start := time.Now()
	for time.Since(start) < 5*time.Second {
		time.Sleep(5 * time.Millisecond)
		title, pos, _, playing := state(t, p)
		if title != "A" {
			// playTrack has published B under p.mu and unlocked; it is at or
			// about to reach C.vibez_start on its local copy of the pointer.
			t.Logf("manual Next visible at t=%v (track=%q pos=%v playing=%v), Close() after %v",
				time.Since(start).Round(time.Millisecond), title, pos, playing, delay)
			if delay > 0 {
				time.Sleep(delay)
			}
			_ = p.Close()
			time.Sleep(1500 * time.Millisecond) // let a late callback land
			t.Logf("RESULT: survived Close() during a manual Next (delay=%v)", delay)
			return
		}
	}
	t.Fatal("manual Next never became observable")
}

// Window 2: ClearQueue() is a second destroyer.
//
// :558-559 calls vibez_stop then vibez_destroy under p.mu, and audioWg is not
// consulted, so clearing during an EOS advance frees the object eosLoop's
// playTrack is about to start. Same polling shape as TestProbeCloseOnTrackChange,
// with ClearQueue in place of Close.
func TestProbeClearQueueOnTrackChange(t *testing.T) {
	dir := musicDir(t)
	delay := closeDelay(t, "PROBE_CLEAR_DELAY_MS", 0)

	// one.mp3 -> two.flac is the pair that reproduced 4/4 on 9cd4c1d.
	first, second := "one.mp3", "two.flac"
	if v := os.Getenv("PROBE_FIRST"); v != "" {
		first = v
	}
	if v := os.Getenv("PROBE_SECOND"); v != "" {
		second = v
	}

	p := newQuiet(t, []provider.Track{track(dir, first, "A"), track(dir, second, "B")})
	defer func() { _ = p.Close() }()
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)

	start := time.Now()
	for time.Since(start) < 12*time.Second {
		time.Sleep(5 * time.Millisecond)
		title, pos, _, playing := state(t, p)
		if title != "A" {
			t.Logf("EOS advance visible at t=%v (track=%q pos=%v playing=%v), ClearQueue() after %v",
				time.Since(start).Round(time.Millisecond), title, pos, playing, delay)
			if delay > 0 {
				time.Sleep(delay)
			}
			_ = p.ClearQueue()
			time.Sleep(1500 * time.Millisecond)
			t.Logf("RESULT: survived ClearQueue() on the track change (delay=%v)", delay)
			return
		}
	}
	t.Fatal("no track change observed")
}

// Window 3: playTrack races itself, with no Close and no ClearQueue.
//
// Each call keeps a local audio and starts it after unlocking, while :311
// destroys p.audio under the lock. Two concurrent calls interleave as: A sets
// p.audio = audioA and unlocks, B destroys audioA and installs audioB, then A
// calls vibez_start(audioA) on freed memory. The p.handle.Delete() and
// cgo.NewHandle(p) pair at :316-318 is exposed the same way.
//
// RepeatModeOne makes every Next() reach playTrack (:380) without depending on
// queue position. PROBE_PLAYTRACK_ROUNDS sets the iteration count.
func TestProbeConcurrentPlayTrack(t *testing.T) {
	dir := musicDir(t)

	rounds := 40
	if v := os.Getenv("PROBE_PLAYTRACK_ROUNDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("PROBE_PLAYTRACK_ROUNDS=%q: %v", v, err)
		}
		rounds = n
	}

	p := newQuiet(t, []provider.Track{
		track(dir, "one.mp3", "A"),
		track(dir, "two.flac", "B"),
	})
	defer func() { _ = p.Close() }()
	_ = p.SetPlaylist("", 0)
	_ = p.SetVolume(0)
	_ = p.SetRepeat(player.RepeatModeOne)
	time.Sleep(300 * time.Millisecond)

	for i := 0; i < rounds; i++ {
		gate := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		for g := 0; g < 2; g++ {
			go func() {
				defer wg.Done()
				<-gate // release both as close together as the runtime allows
				_ = p.Next()
			}()
		}
		close(gate)
		wg.Wait()
		if i%10 == 0 {
			title, pos, _, playing := state(t, p)
			t.Logf("round %d/%d track=%q pos=%v playing=%v", i, rounds, title, pos, playing)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("RESULT: survived %d rounds of concurrent playTrack", rounds)
}
