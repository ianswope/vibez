// PROTOTYPE INSTRUMENT — issue #96 phase E (dual instance). Not shipped.
//
// Companion to TestProbeGapless. That probe measured what one MusicKit
// instance can do; this one measures whether two instances in separate frames
// can prime the next track ahead of the boundary and hand off cleanly.
//
// Skipped unless VIBEZ_PROBE_DUAL=1, so `go test ./...` and CI are unaffected.
//
//	VIBEZ_PROBE_DUAL=1 VIBEZ_PROBE_IDS=1665303761,1665303762,1665303763 \
//	  go test -count=1 ./internal/player/cdp -run TestProbeDual -v -timeout 8m
//
// -count=1 is not optional: Go caches passing test results, so a second run
// with identical env replays the first run's JSON instead of measuring again.
//
// Needs at least 2 ids (E1/E2); a 3rd enables the E3 overlap phase.
// Knobs: VIBEZ_PROBE_LEAD (s before track end, default 6), VIBEZ_PROBE_TAIL
// (ms recorded past the boundary, default 3000), VIBEZ_PROBE_EARLY (ms the
// incoming track starts before the outgoing one ends in E3, default 60),
// VIBEZ_PROBE_DUAL_OUT (result path).
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

//go:embed probe_dual.html
var probeDualHTML string

//go:embed probe_dual_frame.html
var probeDualFrameHTML string

func envInt(key string, def int, min int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= min {
			return n
		}
	}
	return def
}

func TestProbeDual(t *testing.T) {
	if os.Getenv("VIBEZ_PROBE_DUAL") != "1" {
		t.Skip("set VIBEZ_PROBE_DUAL=1 to run the #96 dual-instance probe (needs a real Apple Music session)")
	}

	ids := strings.Split(os.Getenv("VIBEZ_PROBE_IDS"), ",")
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])
	}
	ids = nonEmpty(ids)
	if len(ids) < 2 {
		t.Fatal("VIBEZ_PROBE_IDS needs at least 2 comma-separated catalog song IDs (3 to exercise the overlap phase)")
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

	idsJSON, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("marshal ids: %v", err)
	}

	render := func(name, tmplSrc string, data map[string]any) string {
		tm, err := template.New(name).Parse(tmplSrc)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		var b strings.Builder
		if err := tm.Execute(&b, data); err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		return b.String()
	}

	coordinator := render("dual", probeDualHTML, map[string]any{
		"IDsJSON":     string(idsJSON),
		"LeadSeconds": envInt("VIBEZ_PROBE_LEAD", 6, 2),
		"TailMs":      envInt("VIBEZ_PROBE_TAIL", 3000, 0),
		"EarlyMs":     envInt("VIBEZ_PROBE_EARLY", 60, 0),
	})
	frame := render("frame", probeDualFrameHTML, map[string]any{
		"DeveloperToken": cfg.AppleDeveloperToken,
		"UserToken":      cfg.AppleUserToken,
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	// Both pages must be same-origin: the coordinator reaches into each
	// frame's contentDocument to read its media element directly.
	mux.HandleFunc("/frame", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(frame))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(coordinator))
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

	select {
	case raw := <-resultCh:
		var pretty any
		if err := json.Unmarshal([]byte(raw), &pretty); err != nil {
			t.Fatalf("probe returned unparseable JSON: %v\n%s", err, raw)
		}
		out, _ := json.MarshalIndent(pretty, "", "  ")
		dest := filepath.Clean(os.Getenv("VIBEZ_PROBE_DUAL_OUT"))
		if dest == "." {
			dest = filepath.Join(os.TempDir(), "vibez-probe-96-dual.json")
		}
		//nolint:gosec // G703: dest is an opt-in path from the operator running the probe locally, not untrusted input
		if err := os.WriteFile(dest, out, 0o600); err != nil {
			t.Logf("could not write %s: %v", dest, err)
		} else {
			t.Logf("full probe result written to %s", dest)
		}
		summarizeDual(t, out)
	case <-time.After(7 * time.Minute):
		t.Fatal("dual probe did not report a result within 7m — check that playback actually started")
	}
}

func summarizeDual(t *testing.T, out []byte) {
	t.Helper()
	var r struct {
		Phases []struct {
			Phase                    string   `json:"phase"`
			Skipped                  string   `json:"skipped"`
			Strategy                 string   `json:"strategy"`
			AStartedPlaying          *bool    `json:"aStartedPlaying"`
			AKeptPlayingWhileBPrimed *bool    `json:"aKeptPlayingWhileBPrimed"`
			DistinctMediaElements    *bool    `json:"distinctMediaElements"`
			HandoffFiredAtMs         *float64 `json:"handoffFiredAtMs"`
			StartedEarlyAtMs         *float64 `json:"startedEarlyAtMs"`
			SwappedAtMs              *float64 `json:"swappedAtMs"`
			LayeredMs                *float64 `json:"layeredMs"`
			BPrime                   *struct {
				Warmed      *bool    `json:"warmed"`
				Ready       *int     `json:"ready"`
				BufferedSec *float64 `json:"bufferedSec"`
				ParkedAt    *float64 `json:"parkedAt"`
			} `json:"bPrime"`
			Gap *struct {
				GapMs     *float64 `json:"gapMs"`
				OverlapMs *float64 `json:"overlapMs"`
				Note      string   `json:"note"`
			} `json:"gap"`
		} `json:"phases"`
		Errors []map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Logf("(could not summarize: %v)", err)
		return
	}

	t.Log("──────── #96 dual-instance probe ────────")
	for _, p := range r.Phases {
		if p.Skipped != "" {
			t.Logf("%-22s SKIPPED (%s)", p.Phase, p.Skipped)
			continue
		}
		switch {
		case p.AKeptPlayingWhileBPrimed != nil:
			t.Logf("%-22s two distinct media elements: %v", p.Phase, boolOf(p.DistinctMediaElements))
			t.Logf("%-22s instance A started playing:  %v", p.Phase, boolOf(p.AStartedPlaying))
			t.Logf("%-22s A SURVIVED B priming:        %v   <- concurrency verdict", p.Phase, boolOf(p.AKeptPlayingWhileBPrimed))
			if p.BPrime != nil {
				t.Logf("%-22s B primed: warmed=%v readyState=%v buffered=%vs parkedAt=%vs",
					p.Phase, boolOf(p.BPrime.Warmed), intOf(p.BPrime.Ready), floatOf(p.BPrime.BufferedSec), floatOf(p.BPrime.ParkedAt))
			}
		case p.Gap != nil:
			t.Logf("%-22s %s", p.Phase, p.Strategy)
			if p.Gap.GapMs != nil {
				t.Logf("%-22s GAP = %.1f ms   (overlap %.1f ms)", p.Phase, *p.Gap.GapMs, floatOf(p.Gap.OverlapMs))
			} else {
				t.Logf("%-22s GAP = UNMEASURED (%s)", p.Phase, p.Gap.Note)
			}
			if p.LayeredMs != nil {
				t.Logf("%-22s cost: %.1f ms of the incoming track played under the outgoing one", p.Phase, *p.LayeredMs)
			}
		}
	}
	for _, e := range r.Errors {
		t.Logf("ERROR %v", e)
	}
	t.Log("─────────────────────────────────────────")
}

func boolOf(b *bool) any {
	if b == nil {
		return "n/a"
	}
	return *b
}

func intOf(i *int) any {
	if i == nil {
		return "n/a"
	}
	return *i
}

func floatOf(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}
