// PROTOTYPE INSTRUMENT — issue #96 (seamless transitions). Not shipped.
//
// Lives in this package (rather than under scripts/) for one reason: it must
// launch Chrome through the exact same runPlaywright/chromeLaunchArgs path the
// real player uses. Those are unexported, and a different launch means
// different Widevine/DRM behaviour — which would invalidate the measurement.
//
// It is a test purely so it can reach those helpers. It is skipped unless
// VIBEZ_PROBE=1, so `go test ./...` and CI are unaffected.
//
//	VIBEZ_PROBE=1 VIBEZ_PROBE_IDS=1665303761,1665303762,1665303763 \
//	  go test ./internal/player/cdp -run TestProbeGapless -v -timeout 5m
//
// Pick consecutive tracks from an album you know is mastered gapless; that is
// the case issue #96 is actually about.
//
// Optional knobs: VIBEZ_PROBE_LEAD (seconds before track end to seek to, default
// 6), VIBEZ_PROBE_TAIL (ms to keep recording past the boundary, default 3000 —
// below ~1s the incoming track yields no measurable progress), VIBEZ_PROBE_OUT
// (result path), VIBEZ_PROBE_CONFIG (alternate config file).
package cdp

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	playwright "github.com/mxschmitt/playwright-go"

	"github.com/simone-vibes/vibez/internal/config"
)

//go:embed probe_gapless.html
var probeGaplessHTML string

func TestProbeGapless(t *testing.T) {
	if os.Getenv("VIBEZ_PROBE") != "1" {
		t.Skip("set VIBEZ_PROBE=1 to run the #96 gapless probe (needs a real Apple Music session)")
	}

	ids := strings.Split(os.Getenv("VIBEZ_PROBE_IDS"), ",")
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])
	}
	ids = nonEmpty(ids)
	if len(ids) < 2 {
		t.Fatal("VIBEZ_PROBE_IDS needs at least 2 comma-separated catalog song IDs (consecutive tracks from a gapless album)")
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

	leadSeconds := 6
	if v := os.Getenv("VIBEZ_PROBE_LEAD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 1 {
			leadSeconds = n
		}
	}

	// Recording has to outlast the boundary by enough for the incoming track to
	// accumulate samples; 500ms of tail measures nothing.
	tailMs := 3000
	if v := os.Getenv("VIBEZ_PROBE_TAIL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			tailMs = n
		}
	}

	idsJSON, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("marshal ids: %v", err)
	}
	tmpl, err := template.New("probe").Parse(probeGaplessHTML)
	if err != nil {
		t.Fatalf("parse probe template: %v", err)
	}
	var page strings.Builder
	if err := tmpl.Execute(&page, map[string]any{
		"DeveloperToken": cfg.AppleDeveloperToken,
		"UserToken":      cfg.AppleUserToken,
		"Storefront":     cfg.StoreFront,
		"IDsJSON":        string(idsJSON),
		"LeadSeconds":    leadSeconds,
		"TailMs":         tailMs,
	}); err != nil {
		t.Fatalf("render probe: %v", err)
	}

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
	pg.On("console", func(msg playwright.ConsoleMessage) {
		if ty := msg.Type(); ty == "error" || ty == "warning" {
			t.Logf("[chrome %s] %s", ty, msg.Text())
		}
	})

	if _, err := pg.Goto(fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)); err != nil {
		t.Fatalf("goto: %v", err)
	}

	// Phases A–D each wait on real playback reaching a track boundary.
	select {
	case raw := <-resultCh:
		var pretty any
		if err := json.Unmarshal([]byte(raw), &pretty); err != nil {
			t.Fatalf("probe returned unparseable JSON: %v\n%s", err, raw)
		}
		out, _ := json.MarshalIndent(pretty, "", "  ")

		dest := filepath.Clean(os.Getenv("VIBEZ_PROBE_OUT"))
		if dest == "." {
			dest = filepath.Join(os.TempDir(), "vibez-probe-96.json")
		}
		//nolint:gosec // G703: dest is an opt-in path from the operator running the probe locally, not untrusted input
		if err := os.WriteFile(dest, out, 0o600); err != nil {
			t.Logf("could not write %s: %v", dest, err)
		} else {
			t.Logf("full probe result written to %s", dest)
		}
		summarize(t, out)
	case <-time.After(4 * time.Minute):
		t.Fatal("probe did not report a result within 4m — check that playback actually started")
	}
}

// probeGap is one derivation of the boundary gap. Two are reported per phase:
// a fine one from the media element and a coarse one from MusicKit's
// whole-second position counter.
type probeGap struct {
	GapMs *float64 `json:"gapMs"`
	Note  string   `json:"note"`
}

func gapStr(g *probeGap) string {
	switch {
	case g == nil:
		return "n/a"
	case g.GapMs != nil:
		return fmt.Sprintf("%.1f ms", *g.GapMs)
	default:
		return "UNMEASURED (" + g.Note + ")"
	}
}

// summarize prints the handful of facts the design decision actually turns on,
// so the answer is readable without opening the JSON.
func summarize(t *testing.T, out []byte) {
	t.Helper()
	var r struct {
		MusicKitVersion     string         `json:"musickitVersion"`
		API                 map[string]any `json:"api"`
		MultiItemQueueBuilt bool           `json:"multiItemQueueBuilt"`
		NativeQueueLength   *int           `json:"nativeQueueLength"`
		Phases              []struct {
			Phase        string    `json:"phase"`
			Case         string    `json:"case"`
			Op           string    `json:"op"`
			OK           *bool     `json:"ok"`
			AutoAdvanced *bool     `json:"autoAdvanced"`
			Error        any       `json:"error"`
			QueueChanged *bool     `json:"queueChanged"`
			Gap          *probeGap `json:"gap"`
			GapMedia     *probeGap `json:"gapMedia"`
		} `json:"phases"`
		Enumeration struct {
			InstanceFns  []string `json:"instanceFns"`
			QueueFns     []string `json:"queueFns"`
			NamespaceFns []string `json:"namespaceFns"`
			Interesting  []string `json:"interesting"`
		} `json:"enumeration"`
		Errors []map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Logf("(could not summarize: %v)", err)
		return
	}

	t.Log("──────── #96 probe summary ────────")
	t.Logf("MusicKit version: %s", r.MusicKitVersion)
	if r.NativeQueueLength != nil {
		t.Logf("multi-item setQueue built a native queue of length %d (multi-item: %v)", *r.NativeQueueLength, r.MultiItemQueueBuilt)
	}

	var present, absent []string
	for k, v := range r.API {
		if b, ok := v.(bool); ok {
			if b {
				present = append(present, k)
			} else {
				absent = append(absent, k)
			}
		}
	}
	t.Logf("API present: %s", strings.Join(present, ", "))
	t.Logf("API ABSENT : %s", strings.Join(absent, ", "))

	e := r.Enumeration
	t.Logf("enumerated members: instance %d fns, queue %d fns, MusicKit %d fns",
		len(e.InstanceFns), len(e.QueueFns), len(e.NamespaceFns))
	if len(e.Interesting) == 0 {
		t.Log("prepare/preload/prefetch candidates: NONE — no API to warm the next item")
	} else {
		t.Logf("prepare/preload/prefetch candidates: %s", strings.Join(e.Interesting, ", "))
	}
	t.Logf("instance fns: %s", strings.Join(e.InstanceFns, " "))
	t.Logf("queue fns   : %s", strings.Join(e.QueueFns, " "))

	for _, p := range r.Phases {
		switch {
		case p.GapMedia != nil || p.Gap != nil:
			// The media-element figure is the answer; the coarse
			// currentPlaybackTime one is printed beside it as a sanity check.
			t.Logf("%-22s GAP = %s   (cross-check, +/-1s: %s)", p.Phase, gapStr(p.GapMedia), gapStr(p.Gap))
		case p.Case != "":
			ch := "unchanged"
			if p.QueueChanged != nil && *p.QueueChanged {
				ch = "CHANGED"
			}
			t.Logf("%-22s %-18s err=%v queue=%s", p.Phase, p.Case, p.Error, ch)
		case p.Op != "":
			t.Logf("%-22s %-18s ok=%v err=%v", p.Phase, p.Op, p.OK, p.Error)
		}
		if p.AutoAdvanced != nil {
			t.Logf("%-22s MusicKit auto-advanced without play(): %v", p.Phase, *p.AutoAdvanced)
		}
	}
	for _, e := range r.Errors {
		t.Logf("ERROR %v", e)
	}
	t.Log("───────────────────────────────────")
}

func nonEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
