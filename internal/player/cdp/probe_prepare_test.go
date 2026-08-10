// PROTOTYPE INSTRUMENT — issue #96 phase F (warm without play). Not shipped.
//
// Phase E showed double buffering is blocked only because priming requires
// play(), and play() on any instance pauses every other one. This probe tests
// whether the idle instance can be warmed some other way — via the
// undocumented loadItems / deferPlayback / playbackController* methods, or by
// setQueue alone — while a live stream keeps running.
//
// Reuses the phase-E frame page, so both instances are configured identically.
//
//	VIBEZ_PROBE_PREPARE=1 VIBEZ_PROBE_IDS=1665303761,1665303762 \
//	  go test -count=1 ./internal/player/cdp -run TestProbePrepare -v -timeout 8m
//
// -count=1 is not optional: Go caches passing results and will replay a stale
// run rather than measure again.
//
// Knobs: VIBEZ_PROBE_SETTLE (ms to wait after each attempt before judging it,
// default 1500), VIBEZ_PROBE_PREPARE_OUT (result path).
package cdp

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	playwright "github.com/mxschmitt/playwright-go"

	"github.com/simone-vibes/vibez/internal/config"
)

//go:embed probe_prepare.html
var probePrepareHTML string

func TestProbePrepare(t *testing.T) {
	if os.Getenv("VIBEZ_PROBE_PREPARE") != "1" {
		t.Skip("set VIBEZ_PROBE_PREPARE=1 to run the #96 warm-without-play probe (needs a real Apple Music session)")
	}

	ids := strings.Split(os.Getenv("VIBEZ_PROBE_IDS"), ",")
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])
	}
	ids = nonEmpty(ids)
	if len(ids) < 2 {
		t.Fatal("VIBEZ_PROBE_IDS needs at least 2 comma-separated catalog song IDs")
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

	render := func(name, src string, data map[string]any) string {
		tm, err := template.New(name).Parse(src)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		var b strings.Builder
		if err := tm.Execute(&b, data); err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		return b.String()
	}

	coordinator := render("prepare", probePrepareHTML, map[string]any{
		"IDsJSON":  string(idsJSON),
		"SettleMs": envInt("VIBEZ_PROBE_SETTLE", 1500, 100),
	})
	// Same frame page as phase E — identical configuration on both instances.
	frame := render("frame", probeDualFrameHTML, map[string]any{
		"DeveloperToken": cfg.AppleDeveloperToken,
		"UserToken":      cfg.AppleUserToken,
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
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
		dest := filepath.Clean(os.Getenv("VIBEZ_PROBE_PREPARE_OUT"))
		if dest == "." {
			dest = filepath.Join(os.TempDir(), "vibez-probe-96-prepare.json")
		}
		//nolint:gosec // G703: dest is an opt-in path from the operator running the probe locally, not untrusted input
		if err := os.WriteFile(dest, out, 0o600); err != nil {
			t.Logf("could not write %s: %v", dest, err)
		} else {
			t.Logf("full probe result written to %s", dest)
		}
		summarizePrepare(t, out)
	case <-time.After(7 * time.Minute):
		t.Fatal("prepare probe did not report a result within 7m — check that playback actually started")
	}
}

func summarizePrepare(t *testing.T, out []byte) {
	t.Helper()
	var r struct {
		AStartedPlaying bool `json:"aStartedPlaying"`
		ARecoveries     int  `json:"aRecoveries"`
		Introspection   map[string]struct {
			Arity  *int   `json:"arity"`
			Absent bool   `json:"absent"`
			Source string `json:"source"`
		} `json:"introspection"`
		Attempts []struct {
			Label        string   `json:"label"`
			Error        string   `json:"error"`
			Returned     string   `json:"returned"`
			BWarmed      bool     `json:"bWarmed"`
			BReadyState  *int     `json:"bReadyState"`
			BBufferedSec *float64 `json:"bBufferedSec"`
			ASurvived    bool     `json:"aSurvived"`
			Verdict      string   `json:"verdict"`
		} `json:"attempts"`
		Errors []map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Logf("(could not summarize: %v)", err)
		return
	}

	t.Log("──────── #96 phase F: warm without play ────────")
	t.Logf("instance A started playing: %v   (had to be resumed %d time(s))", r.AStartedPlaying, r.ARecoveries)

	t.Log("— introspection —")
	for _, n := range []string{"loadItems", "deferPlayback", "playbackControllerForItems", "setPlaybackController",
		"getPlaybackController", "getPlaybackControllerByType", "signalIntent", "signalChangeItemIntent",
		"playLater", "playNext", "prepareToPlay", "preloadItem", "queue.appendQueueItems"} {
		v, ok := r.Introspection[n]
		if !ok {
			continue
		}
		if v.Absent {
			t.Logf("  %-32s ABSENT", n)
			continue
		}
		t.Logf("  %-32s arity=%v  %s", n, intOf(v.Arity), firstLine(v.Source))
	}

	t.Log("— attempts (verdict: did it warm B, did A keep playing) —")
	win := ""
	for _, a := range r.Attempts {
		errs := a.Error
		if errs == "" {
			errs = "ok(" + a.Returned + ")"
		}
		t.Logf("  %-44s ready=%v buf=%vs  %-24s  %s",
			a.Label, intOf(a.BReadyState), floatOf(a.BBufferedSec), a.Verdict, errs)
		if a.Verdict == "WARMED-WITHOUT-PAUSING" && win == "" {
			win = a.Label
		}
	}
	if win != "" {
		t.Logf("RESULT: %q warms the idle instance without stopping the live one", win)
	} else {
		t.Log("RESULT: nothing warms an idle instance without stopping the live one")
	}
	for _, e := range r.Errors {
		t.Logf("ERROR %v", e)
	}
	t.Log("────────────────────────────────────────────────")
}

// firstLine compacts a minified function body to something loggable.
func firstLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
