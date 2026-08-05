# PR #62 CoreAudio test plan (macOS side)

Scratch branch: `test/pr62-macos` = upstream PR #62 head (98f04ac) + one commit of
minimal compile fixes so the darwin path can actually be exercised. The fixes are
test scaffolding, not proposed code — the review asks GurKalra to make their own.

Needs: Xcode command-line tools (`xcode-select --install`) and Go.

## 1. Reproduce the upstream breaks first (evidence for the review)

```sh
git fetch upstream pull/62/head && git checkout 98f04ac
CGO_ENABLED=1 go build ./... 2>&1 | head   # expect: undefined "h" in vibezOnEOS; missing SetShuffle via cmd
CGO_ENABLED=0 go build ./... 2>&1 | head   # expect: stub missing LoadTracks/SetQueue/AppendQueue
```

Save the exact output — the review quotes the nocgo errors and predicts the cgo ones from reading;
this turns "from reading" into "reproduced on hardware".

## 2. Build this branch

```sh
git checkout test/pr62-macos
CGO_ENABLED=1 go build -o vibez-local .    # cgo CoreAudio path — UNVERIFIED until this run
CGO_ENABLED=0 go build ./...               # already verified cross-compiling from Linux; should pass
```

If step 1 of the cgo build trips on something residual in the fixup commit, it should be a
one-liner — the interesting part is everything after.

## 3. Runtime matrix

Make a small test dir with one file per format (mp3, flac, m4a, ogg), plus one file
with `#` in its name. Then `./vibez-local --local --music-dir <dir>`.

Core (does the backend work at all):
- [ ] Scan finds all four formats; titles/artists from tags
- [ ] Audio is audible; auto-play starts on launch
- [ ] Pause / resume
- [ ] Next / Previous (Previous >3s in restarts the track)
- [ ] Seek forward and back
- [ ] Volume
- [ ] **EOS auto-advance** — let a track play to the end; this is the new callback path and
      the thing nobody has ever seen run
- [ ] Track duration shows real values (frame-count path, not the Linux 200ms-sleep hack)
- [ ] Quit cleanly — watch for a panic on exit (EOS callback vs deleted cgo.Handle race)

Known-suspect probes (each confirms a review item if it fails):
- [ ] Seek backwards *after* a track has fully ended (suspect: C `done` flag never resets)
- [ ] `:clear` the queue, then try to play anything (expect dead until restart — SetQueue bug)
- [ ] Queue a search-result subset; check Next/queue-panel agreement (SetQueue ignores its id list)
- [ ] Remove/move queue entries during playback
- [ ] The `#`-named file (should be fine on darwin — raw POSIX path, no file:// URI)
- [ ] Relative `--music-dir` (suspect on both platforms)
- [ ] The CGO_ENABLED=0 binary prints the friendly "requires CGo" error and exits

Jot pass/fail inline, commit or photo the notes, bring them back to the Linux session —
the review gets a "tested on Apple Silicon" verdict paragraph either way.
