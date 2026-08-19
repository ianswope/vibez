#!/usr/bin/env bash
# Generate the audio corpus used by the CoreAudio probes in
# internal/player/local/zz_probe*_darwin_test.go.
#
# Deliberately not committed as binaries: seven generated sine files, each a
# distinct pitch so you can tell by ear which track is playing. Six are exactly
# 5.000s; short.mp3 is 0.4s so the EOS boundary arrives quickly.
#
# Usage: scripts/gen-probe-corpus.sh [outdir]     (default: ./probe-corpus)
# Requires: ffmpeg
set -euo pipefail

out="${1:-./probe-corpus}"
command -v ffmpeg >/dev/null || { echo "need ffmpeg (brew install ffmpeg)" >&2; exit 1; }
mkdir -p "$out"

gen() { # file freq title artist [extra ffmpeg args...]
	local file=$1 freq=$2 title=$3 artist=$4; shift 4
	ffmpeg -y -loglevel error -f lavfi -i "sine=frequency=${freq}:duration=5" \
		-metadata title="$title" -metadata artist="$artist" -metadata album="Darwin Probe" \
		"$@" "$out/$file"
}

gen one.mp3        440 "One Four Forty"    "Test Artist A"
gen two.flac       554 "Two Five Fifty"    "Test Artist B"
gen three.m4a      659 "Three Six Fifty"   "Test Artist C"
gen four.ogg       880 "Four Eight Eighty" "Test Artist D"
gen 'five#hash.mp3' 330 "Five Hash"        "Test Artist E"
# 48kHz on purpose: exposes the hardcoded-44100 divisor in duration/position/seek.
gen six_48k.flac   740 "Six 48k"           "Test Artist F" -ar 48000
# 0.4s on purpose: the Close/EOS probes need a boundary that arrives quickly.
gen short.mp3      494 "Short Four Ninety"  "Test Artist G" -t 0.4

echo "corpus written to $out"
ls -1 "$out"
