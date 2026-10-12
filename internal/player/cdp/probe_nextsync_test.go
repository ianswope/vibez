// PROTOTYPE INSTRUMENT, issue #161. Not shipped.
//
// Measures whether keeping MusicKit's next item in sync via playNext works.
// See probe_nextsync.html for the questions. Same launch path as the #96 probes:
//
//	VIBEZ_PROBE=1 VIBEZ_PROBE_IDS=a,b,c,d,e \
//	  go test ./internal/player/cdp -run TestProbeNextSync -v -count=1 -timeout 20m
package cdp

import (
	"bytes"
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

//go:embed probe_nextsync.html
var probeNextSyncHTML string

func TestProbeNextSync(t *testing.T) {
	if os.Getenv("VIBEZ_PROBE") != "1" {
		t.Skip("set VIBEZ_PROBE=1 to run the #96 items-queue probe (needs a real Apple Music session)")
	}

	ids := strings.Split(os.Getenv("VIBEZ_PROBE_IDS"), ",")
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])
	}
	ids = nonEmpty(ids)
	if len(ids) < 5 {
		t.Fatal("VIBEZ_PROBE_IDS needs at least 5 comma-separated catalog song IDs")
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
	tailMs := 3000
	if v := os.Getenv("VIBEZ_PROBE_TAIL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			tailMs = n
		}
	}

	reps := 3
	if v := os.Getenv("VIBEZ_PROBE_REPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			reps = n
		}
	}

	idsJSON, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("marshal ids: %v", err)
	}
	tmpl, err := template.New("probeNextSync").Parse(probeNextSyncHTML)
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
		"Reps":           reps,
		"Only":           os.Getenv("VIBEZ_PROBE_ONLY"),
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

	select {
	case raw := <-resultCh:
		var pretty any
		if err := json.Unmarshal([]byte(raw), &pretty); err != nil {
			t.Fatalf("probe returned unparseable JSON: %v\n%s", err, raw)
		}
		out, _ := json.MarshalIndent(pretty, "", "  ")

		dest := filepath.Clean(os.Getenv("VIBEZ_PROBE_OUT"))
		if dest == "." {
			dest = filepath.Join(os.TempDir(), "vibez-probe-161-nextsync.json")
		}
		//nolint:gosec // G703: dest is an opt-in path from the operator running the probe locally, not untrusted input
		if err := os.WriteFile(dest, out, 0o600); err != nil {
			t.Logf("could not write %s: %v", dest, err)
		} else {
			t.Logf("full probe result written to %s", dest)
		}
		summarizeNextSync(t, out)
	case <-time.After(15 * time.Minute):
		t.Fatal("probe did not report a result within 15m — check that playback actually started")
	}
}

// summarizeNextSync prints each phase as one JSON line.
func summarizeNextSync(t *testing.T, out []byte) {
	t.Helper()
	var r struct {
		MusicKitVersion string            `json:"musickitVersion"`
		Resolved        json.RawMessage   `json:"resolvedCatalog"`
		LibrarySource   string            `json:"libraryItemSource"`
		Library         json.RawMessage   `json:"libraryItem"`
		Phases          []json.RawMessage `json:"phases"`
		Errors          json.RawMessage   `json:"errors"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Logf("could not summarize: %v", err)
		return
	}
	t.Logf("MusicKit %s", r.MusicKitVersion)
	t.Logf("catalog: %s", r.Resolved)
	t.Logf("library item (%s): %s", r.LibrarySource, r.Library)
	for _, p := range r.Phases {
		var c bytes.Buffer
		_ = json.Compact(&c, p)
		t.Logf("phase: %s", c.String())
	}
	t.Logf("errors: %s", r.Errors)
}
