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
