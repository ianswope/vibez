// PROTOTYPE INSTRUMENT — issue #96 (seamless transitions). Not shipped.
//
// Plays an album in the queue shape vibez ships and reads MusicKit's own
// seamless-transition state at each boundary (see probe_seamless.html), while
// recording what actually comes out of the speakers. The recording is the
// ground truth: it is the only measurement here that cannot mistake a 50 ms
// hole for a seamless join.
//
//	VIBEZ_PROBE=1 VIBEZ_PROBE_ALBUM=1065973699 \
//	  go test ./internal/player/cdp -run TestProbeSeamless -v -count=1 -timeout 20m
//
// Knobs: VIBEZ_PROBE_STARTMODE (changeTo, as vibez does, or startWith),
// VIBEZ_PROBE_SHIFT_MS (start each next song this much earlier),
// VIBEZ_PROBE_START (0-based track index, default 0), VIBEZ_PROBE_COUNT
// (boundaries to cross, default 4), VIBEZ_PROBE_NATURAL=1 (never seek; real
// listening, real duration), VIBEZ_PROBE_FEATURES (comma list passed to
// MusicKit.configure, e.g. "-seamless-audio-transitions" for the control arm),
// VIBEZ_PROBE_LEAD, VIBEZ_PROBE_TAIL, VIBEZ_PROBE_OUT (JSON path; the WAV is
// written beside it), VIBEZ_PROBE_SINK (PulseAudio source to record, default
// the default sink's monitor).
//
// Records system output, so anything else playing lands in the analysis.
// Every track it starts lands in the account's Recently Played.
package cdp

import (
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"text/template"
	"time"

	playwright "github.com/mxschmitt/playwright-go"

	"github.com/simone-vibes/vibez/internal/config"
)

//go:embed probe_seamless.html
var probeSeamlessHTML string

func seamlessEnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

func TestProbeSeamless(t *testing.T) {
	if os.Getenv("VIBEZ_PROBE") != "1" {
		t.Skip("set VIBEZ_PROBE=1 to run the #96 seamless probe (needs a real Apple Music session)")
	}
	album := strings.TrimSpace(os.Getenv("VIBEZ_PROBE_ALBUM"))
	if album == "" {
		t.Fatal("VIBEZ_PROBE_ALBUM needs a catalog album ID")
	}
	var features []string
	for _, f := range strings.Split(os.Getenv("VIBEZ_PROBE_FEATURES"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			features = append(features, f)
		}
	}
	if features == nil {
		features = []string{}
	}

	cfg, err := config.Load(os.Getenv("VIBEZ_PROBE_CONFIG"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AppleDeveloperToken == "" || strings.Count(cfg.AppleDeveloperToken, ".") != 2 {
		t.Fatalf("config has no usable developer token (got %d chars). Run `make refresh-token` first.", len(cfg.AppleDeveloperToken))
	}
	if cfg.AppleUserToken == "" {
		t.Fatal("config has no Apple user token — log in once with vibez, then re-run")
	}

	count := seamlessEnvInt("VIBEZ_PROBE_COUNT", 4)
	startMode := "changeTo"
	if os.Getenv("VIBEZ_PROBE_STARTMODE") == "startWith" {
		startMode = "startWith"
	}
	shiftMs := 0.0
	if v := os.Getenv("VIBEZ_PROBE_SHIFT_MS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			shiftMs = f
		}
	}
	featuresJSON, _ := json.Marshal(features)
	tmpl, err := template.New("probeSeamless").Parse(probeSeamlessHTML)
	if err != nil {
		t.Fatalf("parse probe template: %v", err)
	}
	var page strings.Builder
	if err := tmpl.Execute(&page, map[string]any{
		"DeveloperToken": cfg.AppleDeveloperToken,
		"UserToken":      cfg.AppleUserToken,
		"Storefront":     cfg.StoreFront,
		"Album":          album,
		"Start":          seamlessEnvInt("VIBEZ_PROBE_START", 0),
		"Count":          count,
		"Natural":        os.Getenv("VIBEZ_PROBE_NATURAL") == "1",
		"FeaturesJSON":   string(featuresJSON),
		"LeadSeconds":    seamlessEnvInt("VIBEZ_PROBE_LEAD", 6),
		"TailMs":         seamlessEnvInt("VIBEZ_PROBE_TAIL", 3000),
		"StartMode":      startMode,
		"ShiftMs":        shiftMs,
		"HlsJs":          os.Getenv("VIBEZ_PROBE_HLSJS") == "1",
		"Squash":         os.Getenv("VIBEZ_PROBE_SQUASH") == "1",
		"Kick":           os.Getenv("VIBEZ_PROBE_KICK") == "1",
	}); err != nil {
		t.Fatalf("render probe: %v", err)
	}

	dest := filepath.Clean(os.Getenv("VIBEZ_PROBE_OUT"))
	if dest == "." {
		dest = filepath.Join(os.TempDir(), "vibez-probe-96-seamless.json")
	}
	wavPath := strings.TrimSuffix(dest, filepath.Ext(dest)) + ".wav"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page.String()))
	})
	srv := &http.Server{Handler: mux, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	if err := EnsureBrowser(func(s string) { t.Logf("browser setup: %s", s) }); err != nil {
		t.Fatalf("ensure browser: %v", err)
	}
	pw, err := runPlaywright()
	if err != nil {
		t.Fatalf("playwright: %v", err)
	}
	defer func() { _ = pw.Stop() }()

	chromePath := HelperPath()
	if _, err := os.Stat(chromePath); err != nil {
		chromePath = ChromePath()
	}
	headless := true
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		ExecutablePath:    &chromePath,
		Headless:          &headless,
		IgnoreDefaultArgs: []string{"--mute-audio", "--disable-component-update"},
		Args:              chromeLaunchArgs(headless, false),
	})
	if err != nil {
		t.Fatalf("launch chrome: %v", err)
	}
	defer func() { _ = browser.Close() }()

	pg, err := browser.NewPage()
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	resultCh := make(chan string, 1)
	if err := pg.ExposeFunction("goProbeResult", func(args ...any) any {
		if len(args) > 0 {
			if s, ok := args[0].(string); ok {
				select {
				case resultCh <- s:
				default:
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("expose goProbeResult: %v", err)
	}
	pg.On("pageerror", func(err error) { t.Logf("page error: %v", err) })

	// Start recording before the page exists, so the first boundary can never
	// predate the WAV. parecord takes ~2 s to deliver its first sample from a
	// suspended sink, so the WAV is anchored to when it was stopped, not when
	// it was started: the last sample is recStop, the first is recStop minus
	// the recording's length.
	source := os.Getenv("VIBEZ_PROBE_SINK")
	if source == "" {
		source = "@DEFAULT_MONITOR@"
	}
	//nolint:gosec // G204: operator-supplied source name for a local opt-in probe
	rec := exec.Command("parecord", "--device="+source, "--file-format=wav",
		"--format=s16le", "--rate=48000", "--channels=2", wavPath)
	if err := rec.Start(); err != nil {
		t.Fatalf("start parecord: %v", err)
	}
	var recStop time.Time
	stopRec := func() {
		if !recStop.IsZero() {
			return
		}
		recStop = time.Now()
		_ = rec.Process.Signal(syscall.SIGINT)
		_ = rec.Wait()
	}
	defer stopRec()

	if _, err := pg.Goto(fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)); err != nil {
		t.Fatalf("goto: %v", err)
	}

	budget := time.Duration(count*12+5) * time.Minute
	if os.Getenv("VIBEZ_PROBE_NATURAL") != "1" {
		budget = time.Duration(count+3) * time.Minute
	}
	var raw string
	select {
	case raw = <-resultCh:
	case <-time.After(budget):
		t.Fatalf("probe did not report a result within %s", budget)
	}
	stopRec()

	var r seamlessResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("probe returned unparseable JSON: %v", err)
	}
	pcm, rate, err := readWAV(wavPath)
	if err != nil {
		t.Logf("audio analysis skipped: %v", err)
	}
	for i := range r.Boundaries {
		b := &r.Boundaries[i]
		if pcm == nil || b.CrossWall == 0 {
			continue
		}
		recStart := recStop.Add(-time.Duration(float64(len(pcm)) / float64(rate) * float64(time.Second)))
		at := time.UnixMilli(b.CrossWall).Sub(recStart)
		b.Audio = silenceAround(pcm, rate, at)
	}

	out, _ := json.MarshalIndent(r, "", "  ")
	//nolint:gosec // G703: dest is an opt-in path from the operator running the probe locally
	if err := os.WriteFile(dest, out, 0o600); err != nil {
		t.Logf("could not write %s: %v", dest, err)
	} else {
		t.Logf("full result: %s   audio: %s", dest, wavPath)
	}

	t.Logf("MusicKit %s, features %v, start %s, shift %.1f ms", r.MusicKitVersion, features, r.StartMode, r.ShiftMs)
	for _, b := range r.Boundaries {
		name := func(id string) string {
			for _, tr := range r.Tracks {
				if tr.ID == id {
					return fmt.Sprintf("%d %q", tr.I+1, tr.Name)
				}
			}
			return id
		}
		mode := "seek"
		if b.Natural {
			mode = "natural"
		}
		t.Logf("%s -> %s [%s] crossed=%v", name(b.FromID), name(b.ToID), mode, b.Crossed)
		if b.Error != "" {
			t.Logf("   error: %s", b.Error)
			continue
		}
		t.Logf("   seamless=%v nextManifest=%v transitionScheduled=%v srcs=%d elements=%d",
			deref(b.SeamlessEnabled), derefS(b.NextManifestBefore), b.TransitionScheduled, b.DistinctSrcs, b.MediaElements)
		if a := b.Audio; a != nil {
			t.Logf("   audio: longest silence %.0f ms at %+.0f ms from item change (threshold %.0f dBFS, window %s)",
				a.LongestSilenceMs, a.SilenceAtMs, a.ThresholdDB, a.Window)
		}
	}
	for _, e := range r.Errors {
		t.Logf("ERROR %s: %s", e.Stage, e.Error)
	}
}

type seamlessResult struct {
	MusicKitVersion string   `json:"musickitVersion"`
	Features        []string `json:"features"`
	PlayStartWall   int64    `json:"playStartWall"`
	StartMode       string   `json:"startMode"`
	ShiftMs         float64  `json:"shiftMs"`
	// Every append, timestampOffset write, key update and media-element
	// event, from the instrumentation in probe_seamless.html.
	Events json.RawMessage `json:"events"`
	Tracks []struct {
		I     int    `json:"i"`
		ID    string `json:"id"`
		Name  string `json:"name"`
		DurMs int    `json:"durMs"`
	} `json:"tracks"`
	Boundaries []struct {
		K                   int             `json:"k"`
		FromIdx             int             `json:"fromIdx"`
		FromID              string          `json:"fromId"`
		ToID                string          `json:"toId"`
		Natural             bool            `json:"natural"`
		Crossed             bool            `json:"crossed"`
		CrossWall           int64           `json:"crossWall"`
		Seek35Wall          int64           `json:"seek35Wall,omitempty"`
		SeekEndWall         int64           `json:"seekEndWall,omitempty"`
		DistinctSrcs        int             `json:"distinctMediaSrcs"`
		MediaElements       int             `json:"mediaElements"`
		SeamlessEnabled     *bool           `json:"seamlessEnabled"`
		NextManifestBefore  *string         `json:"nextManifestBefore"`
		TransitionScheduled bool            `json:"transitionScheduled"`
		Error               string          `json:"error,omitempty"`
		Audio               *silenceReport  `json:"audio,omitempty"`
		Timeline            json.RawMessage `json:"timeline"`
	} `json:"boundaries"`
	Errors []struct {
		Stage string `json:"stage"`
		Error string `json:"error"`
	} `json:"errors"`
}

type silenceReport struct {
	Window           string  `json:"window"`
	ThresholdDB      float64 `json:"thresholdDb"`
	LongestSilenceMs float64 `json:"longestSilenceMs"`
	SilenceAtMs      float64 `json:"silenceAtMsFromItemChange"`
	// Peak level per 10 ms across the window, for eyeballing the join.
	Envelope []float64 `json:"envelopeDb10ms"`
}

func deref(b *bool) any {
	if b == nil {
		return "?"
	}
	return *b
}

func derefS(s *string) string {
	if s == nil {
		return "none"
	}
	return *s
}

// readWAV returns interleaved-to-mono peak samples (max |L|,|R|) as float in
// [0,1] from a 16-bit PCM WAV.
func readWAV(path string) ([]float64, int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: probe's own output
	if err != nil {
		return nil, 0, err
	}
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("%s: not a WAV", path)
	}
	var rate, channels, bits int
	for off := 12; off+8 <= len(data); {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := data[off+8:]
		switch id {
		case "fmt ":
			channels = int(binary.LittleEndian.Uint16(body[2:4]))
			rate = int(binary.LittleEndian.Uint32(body[4:8]))
			bits = int(binary.LittleEndian.Uint16(body[14:16]))
		case "data":
			if bits != 16 || channels < 1 {
				return nil, 0, fmt.Errorf("unsupported WAV: %d-bit, %d ch", bits, channels)
			}
			// parecord stopped by SIGINT may leave the size unpatched.
			if size == 0 || size > len(body) {
				size = len(body)
			}
			frames := size / (2 * channels)
			out := make([]float64, frames)
			for f := 0; f < frames; f++ {
				peak := 0.0
				for c := 0; c < channels; c++ {
					v := math.Abs(float64(int16(binary.LittleEndian.Uint16(body[(f*channels+c)*2:])))) / 32768
					peak = math.Max(peak, v)
				}
				out[f] = peak
			}
			return out, rate, nil
		}
		off += 8 + size + size%2
	}
	return nil, 0, fmt.Errorf("%s: no data chunk", path)
}

// silenceAround finds the longest run of 1 ms blocks whose peak stays under
// -60 dBFS within [-2.5 s, +2 s] of the item-change time. The window is wide
// because nowPlayingItemDidChange and the audible join need not coincide, and
// the recorder's start latency shifts everything slightly.
func silenceAround(pcm []float64, rate int, at time.Duration) *silenceReport {
	const thresholdDB = -60.0
	thr := math.Pow(10, thresholdDB/20)
	from := at - 2500*time.Millisecond
	to := at + 2000*time.Millisecond
	blk := rate / 1000
	i0 := max(0, int(from.Seconds()*float64(rate)))
	i1 := min(len(pcm), int(to.Seconds()*float64(rate)))
	rep := &silenceReport{Window: "-2500..+2000 ms", ThresholdDB: thresholdDB}
	if i1-i0 < blk {
		return rep
	}
	run, best, bestEnd := 0, 0, 0
	for i := i0; i+blk <= i1; i += blk {
		peak := 0.0
		for _, v := range pcm[i : i+blk] {
			peak = math.Max(peak, v)
		}
		if peak < thr {
			run++
			if run > best {
				best, bestEnd = run, i+blk
			}
		} else {
			run = 0
		}
	}
	rep.LongestSilenceMs = float64(best)
	if best > 0 {
		startSample := bestEnd - best*blk
		rep.SilenceAtMs = (float64(startSample)/float64(rate) - at.Seconds()) * 1000
	}
	step := rate / 100
	for i := i0; i+step <= i1; i += step {
		peak := 1e-9
		for _, v := range pcm[i : i+step] {
			peak = math.Max(peak, v)
		}
		rep.Envelope = append(rep.Envelope, math.Round(20*math.Log10(peak)*10)/10)
	}
	return rep
}
