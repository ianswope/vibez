# PR #62 CoreAudio test plan + results (macOS side)

Scratch branch for exercising the darwin/CoreAudio half of upstream PR #62 on real
Apple hardware. Base is PR head `b306c6a`.

Two commits sit on top of that head:

1. **the harness** — `internal/player/local/zz_probe*_darwin_test.go` plus
   `scripts/gen-probe-corpus.sh`. Touches no PR code, so it **cherry-picks cleanly onto
   any future PR head**. This is the reusable part.
2. **the scaffolding** — the two edits to `player_darwin.go` that make the darwin path
   compile at all (corrected `vibezOnEOS` signature, one-line `SetShuffle`). Head-specific
   and expected to conflict once GurKalra fixes these themselves; drop it then.

The scaffolding is test scaffolding, **not proposed code** — the review asks GurKalra to
make their own calls on the implementation.

Needs: Xcode command-line tools (`xcode-select --install`), Go, and ffmpeg for the corpus.

## Re-running against a new PR head

```sh
git fetch upstream pull/62/head:pr62-current
git switch -c test/pr62-<newhead> pr62-current
git cherry-pick <harness-commit>          # commit 1 above; portable
CGO_ENABLED=1 go build ./...              # if this fails, scaffolding is still needed
CGO_ENABLED=0 go build ./...              # the nocgo stub path

scripts/gen-probe-corpus.sh /tmp/probe-corpus
export PROBE_MUSIC_DIR=/tmp/probe-corpus
CGO_ENABLED=1 go test ./internal/player/local/ -run TestProbe -v -timeout 300s
```

Probes force volume to 0 and drive the `Player` API directly, so no TUI and no noise.
They log observed values rather than asserting hard — read the output, don't just trust
the pass/fail. Run `TestProbeCloseWhilePlaying` and `TestProbeHandleChurn` in their own
process: a panic on a cgo callback thread is unrecoverable and takes the whole run down,
which is itself the signal.

`TestProbe*` all skip unless `PROBE_MUSIC_DIR` is set, so this is safe to leave in a tree
that CI builds.

## Results as of `b306c6a` (2026-08-05, M-series, macOS 25.5, clang 21.0.0, go darwin/arm64)

Posted upstream: https://github.com/simonepelosi/vibez/pull/62#issuecomment-5187482545

Build — **both paths fail**:
- `CGO_ENABLED=1`: `conflicting types for 'vibezOnEOS'`. Preamble says
  `extern void vibezOnEOS(uintptr_t)`, Go says `func vibezOnEOS(ptr unsafe.Pointer)`
  (cgo emits `void*`). `b306c6a` claims to have fixed this but only edited the body.
- latent behind it: the body reads `h`, the parameter is named `ptr`.
- after fixing the signature: `missing method SetShuffle` — the only interface method the
  Linux GStreamer player has and CoreAudio lacks (diffed all 25).
- `CGO_ENABLED=0`: `LoadTracks` / `SetQueue` / `AppendQueue` undefined. The nocgo stub is
  11 lines: `type Player struct{}` + `New()`.
- No CI has ever run on this branch.

Runtime, after the scaffolding:

| Item | Result |
|---|---|
| **EOS auto-advance** | **works** — fires at 5.0s, A→B |
| mp3 / flac / m4a scan, tags, playback | works |
| `#` in filename | works (raw POSIX path, no `file://`) |
| `go test ./internal/provider/local/` | passes on darwin |
| Seek | **kills playback permanently**; pos freezes at target, `Playing` stays true |
| Duration/position at 48kHz | 5.44217687s for a 5.000s file (= 5×48000/44100) |
| Position accuracy | decode cursor, not playback; starts at 557.278911ms, ~0.55s ahead |
| `.ogg` | scanned but unplayable (no Vorbis decoder); 0 frames, instant EOS, skipped <1s |
| Repeat-off, single track | loops forever (`Next()` wraps `% len(queue)`) |
| `SetQueue` id list | ignored beyond `ids[0]`; queue not replaced |
| Play after `ClearQueue` | queue unrecoverable without restart |
| `pollState` position | stale; Linux refreshes it, darwin doesn't |
| cgo.Handle race | **not reproduced** — 300 transitions, 6 EOS collisions, Close-at-EOS |

Two numbers worth keeping, because they pin the root causes exactly:
`5 × 48000/44100 = 5.44217687` (the hardcoded divisor) and
`3 buffers × 32768 B ÷ 4 B/frame ÷ 44100 = 557.3ms` (the prefill, i.e. every track's
starting position).

Seek turned out worse than originally suspected: not "dead after EOS" but dead always.
`vibez_seek` does `AudioQueueStop(queue, true)` then `AudioQueueStart` without
re-enqueueing buffers — an immediate stop returns them all, so the output callback is
never invoked again.

## Still needing a human

- **Is audio actually audible, and does it sound correct?** Probes run at volume 0.
  Run `./vibez --local --music-dir <corpus>` and listen.
- Interactive TUI behaviour: queue panel agreement, `:clear`, entry remove/move during
  playback, relative `--music-dir`.

---

## Results as of `9cd4c1d` (2026-08-18, M-series Apple Silicon, go1.26.1 darwin/arm64)

Re-run of the same matrix against the current PR head, 15 commits on from `b306c6a`.
The harness commit cherry-picked with no conflict; **the scaffolding commit was not
needed and was dropped** — GurKalra's own code compiles now.

Build — **both paths clean, no local edits**:

| Check | Result |
|---|---|
| `CGO_ENABLED=1 go build ./...` | clean (was: `conflicting types for 'vibezOnEOS'`) |
| `CGO_ENABLED=0 go build ./...` | clean (was: `LoadTracks`/`SetQueue`/`AppendQueue` undefined) |
| `go vet ./...` | clean |
| `go test ./...` | 17 packages ok, 0 failures |
| `gofmt -l` | empty |

Runtime, `b306c6a` → `9cd4c1d`:

| Item | Then | Now |
|---|---|---|
| Seek mid-playback | dead permanently | **fixed** — pos advances 678ms → 2.786s, playing stays true |
| Seek after EOS | dead | resumes, pos 1.4808s — but `Playing=false` while position moved (see below) |
| Duration at 48kHz | 5.44217687s for a 5.000s file | **fixed** — 5s (divisor no longer hardcoded) |
| Position accuracy | started at 557.278911ms, ran ~0.55s ahead | **fixed** — 1.478s at t=1.5s, ~22ms behind wall clock |
| `SetQueue` id list | ignored beyond `ids[0]` | **fixed** — `SetQueue([C,B])` → len=2, plays TRACK-C |
| Play after `ClearQueue` | queue unrecoverable | **fixed** — Clear+Set+Append → len=3, playing |
| EOS auto-advance | works | works (TRACK-A → TRACK-B) |
| mp3 / flac / m4a, `#` in filename | works | works |
| `.ogg` | scanned, unplayable, skipped | **unchanged** — dur=0s, skipped after ~0.5–1.0s |
| Repeat-off, single track | loops forever | **unchanged** — restarted 1×; `Next()` still wraps `% len(queue)` |
| cgo.Handle / EOS race | not reproduced | **reproduced as a use-after-free SIGSEGV — see below** |
| `pollState` stale position | stale vs Linux | not probe-covered this round |

### New: `Close()` during an EOS-driven track change is a use-after-free

`playTrack` releases `p.mu` and *then* calls `C.vibez_start(audio)` on a local copy of
the pointer (`player_darwin.go:327`). `Close` takes `p.mu` and calls
`C.vibez_destroy(p.audio)` on the same object (`player_darwin.go:594`). The mutex guards
the *field*, not the in-flight C call, so a `Close` landing in that window frees the
AudioQueue/ExtAudioFile out from under a running `vibez_start`. One crash dump has both
frames on the same address: `_Cfunc_vibez_start(0x81b548140)` and
`_Cfunc_vibez_destroy(0x81b548140)`.

Reproduction rates, `TestProbeCloseOnTrackChange` (poll every 5ms, `Close()` the instant
the change is observable — `pos=0s` in the state read is the marker for being inside the
window):

| Corpus | Rate |
|---|---|
| one.mp3 (5s) → two.flac (5s) | **4/4** |
| short.mp3 (0.4s) → two.flac | 4/6 |
| four.ogg → one.mp3 (`TestProbeOggNeverEnds`) | 5/5 |

The ogg is **not** the cause — it only phase-locks the collision with that probe's 750ms
poll, which is why it looked deterministic there. Two controls separate the race from
state corruption: the same advance with no `Close()` survives (`TestProbeOggNoClose`), and
a fixed-delay `Close()` landing *after* the window survives 10/10 at 300/600/900/1500/3000ms
(`TestProbeCloseAfterOggAdvance`, `TestProbeCloseAtEOSStart`).

In the app this is quit-during-auto-advance: press `q` as a track rolls over and the
process can die in CoreAudio instead of exiting.

### Observation, not yet a defect: EOS fires ~200ms early

With a 5ms poll, the change off `one.mp3` (duration reported 5.041632653s) is visible at
t≈4.845s — about 197ms before the audio should end. Probes run at volume 0, so whether the
tail is actually truncated needs a human to listen.

### Still needing a human

Unchanged from the `b306c6a` run: is audio audible and correct, and does the interactive
TUI agree (queue panel, `:clear`, remove/move during playback, relative `--music-dir`).

## Round 3: the windows `a34eae3` leaves open

`a34eae3` adds an `audioWg sync.WaitGroup` that `Close` waits on before
`vibez_destroy`. It closes the reported repro, because `eosLoop`'s only exit is the
`<-p.done` case, so `Wait()` (`player_darwin.go:595`) cannot return while `eosLoop` is
inside `playTrack`. The WaitGroup counts `eosLoop` and nothing else, so three windows
onto the same free remain. `zz_probe6_darwin_test.go` covers them.

Run each alone. The fault is unrecoverable and takes the process down, so a shared run
cannot tell you which probe died.

```sh
export PROBE_MUSIC_DIR="$PWD/probe-corpus"   # must be absolute, see below
go test ./internal/player/local/ -run TestProbeCloseDuringManualNext   -v -count=1
go test ./internal/player/local/ -run TestProbeClearQueueOnTrackChange -v -count=1
go test ./internal/player/local/ -run TestProbeConcurrentPlayTrack     -v -count=1
```

| Probe | Window | Destroyer | Reached from |
|---|---|---|---|
| `TestProbeCloseDuringManualNext` | user `Next()` vs `Close()` | `Close` `:598` | `Next` `:395`, not `eosLoop` |
| `TestProbeClearQueueOnTrackChange` | EOS advance vs `ClearQueue()` | `ClearQueue` `:558-559` | `eosLoop`, destroyer the WaitGroup ignores |
| `TestProbeConcurrentPlayTrack` | `playTrack` vs `playTrack` | `playTrack` `:311` | two callers, no `Close` at all |

The first is "press `n`, then `q`". The TUI puts those on different goroutines by
construction: `n` dispatches `Next` through `playerCmd` (`internal/tui/model.go:2095`) as
a `tea.Cmd`, which bubbletea runs off the event loop, while `Close` is called
synchronously in `handleKey` (`:1162`) and `executeCommand` (`:1426`) on the event loop.

Controls, matching round 2: `PROBE_NEXT_CLOSE_DELAY_MS` and `PROBE_CLEAR_DELAY_MS` push
the call past the post-unlock window, and a surviving delayed run separates a race from
state corruption. `PROBE_PLAYTRACK_ROUNDS` sets the concurrent-`playTrack` iteration
count (default 40).

`PROBE_PLAYTRACK_ROUNDS` is **not** a control for window 3. Every round launches
`PROBE_PLAYTRACK_GOROUTINES` concurrent `Next()` calls (default 2), so `ROUNDS=1` still
races two of them and still dies. `PROBE_PLAYTRACK_GOROUTINES=1` is the control: same
total call count, no concurrency.

### Absolute corpus path

`PROBE_MUSIC_DIR` must be absolute. `go test` runs each test binary in its own package
directory, so a relative `./probe-corpus` resolves against `internal/player/local/` rather
than the repo root. The old failure was silent, and it disarmed every probe in the suite:
track IDs pointed at nothing, `LoadTracks` produced an empty queue, `SetPlaylist` returned
`nil` at its `len(p.queue)==0` guard (`player_darwin.go:473`), and each `title != "A"`
guard then fired at t≈6ms against `track="" playing=false`, reporting "survived" without
reaching the window. The tell in a log is `pos=0s` next to an empty title.

`musicDir` now resolves the path, refuses a directory it cannot read, and `mustPlay` /
`mustStartPlaying` fail any probe whose playback never started. Round 1 and 2 used the
absolute `/tmp/probe-corpus` above, so their published numbers are unaffected; only the
round-3 snippet was wrong.

### Corpus fix

`gen-probe-corpus.sh` generated six 5.000s files and no `short.mp3`, which
`TestProbeCloseAtEOSStart` and `TestProbeCloseOnTrackChange` both default to. On a freshly
generated corpus those two opened a missing file, so they reported "survived" without ever
reaching the window. The generator now also writes `short.mp3` at 0.4s, verified at
exactly 0.400000s. `TestProbeClearQueueOnTrackChange` defaults to `one.mp3` and
`two.flac`, the pair that reproduced 4/4 on `9cd4c1d`.
