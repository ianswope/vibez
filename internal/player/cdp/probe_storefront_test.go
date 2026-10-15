// PROTOTYPE INSTRUMENT, issue #163. Not shipped.
//
// Shows where music.storefrontId comes from when musickit.html applies a saved
// user token without authorize(). See probe_storefront.html for the questions.
// Same launch path as the #96 and #161 probes:
//
//	VIBEZ_PROBE=1 VIBEZ_PROBE_CONFIG=~/.config/vibez/config.json \
//	  go test ./internal/player/cdp -run TestProbeStorefront -v -count=1 -timeout 10m
//
// It runs twice in fresh browsers: once as vibez does, and once with MusicKit's
// persisted storefront key seeded to VIBEZ_PROBE_SEED (default "ar") before
// configure, which stands in for a non-US account on a US machine.
package cdp

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	playwright "github.com/mxschmitt/playwright-go"

	"github.com/simone-vibes/vibez/internal/config"
)

//go:embed probe_storefront.html
var probeStorefrontHTML string

func TestProbeStorefront(t *testing.T) {
	if os.Getenv("VIBEZ_PROBE") != "1" {
		t.Skip("set VIBEZ_PROBE=1 to run the #163 storefront probe (needs a real Apple Music session)")
	}

	cfg, err := config.Load(os.Getenv("VIBEZ_PROBE_CONFIG"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AppleDeveloperToken == "" || strings.Count(cfg.AppleDeveloperToken, ".") != 2 {
		t.Fatalf("config has no usable developer token (got %d chars). Run `make refresh-token` first.", len(cfg.AppleDeveloperToken))
	}
	if cfg.AppleUserToken == "" {
		t.Fatal("config has no Apple user token: log in once with vibez, then re-run")
	}
	seed := os.Getenv("VIBEZ_PROBE_SEED")
	if seed == "" {
		seed = "ar"
	}

	tmpl, err := template.New("probeStorefront").Parse(probeStorefrontHTML)
	if err != nil {
		t.Fatalf("parse probe template: %v", err)
	}

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

	for _, variant := range []string{"", seed} {
		name := "unseeded"
		if variant != "" {
			name = "seeded-" + variant
		}
		t.Run(name, func(t *testing.T) {
			var page strings.Builder
			if err := tmpl.Execute(&page, map[string]any{
				"DeveloperToken": cfg.AppleDeveloperToken,
				"UserToken":      cfg.AppleUserToken,
				"Seed":           variant,
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

			// A fresh browser per variant: the default Launch profile is
			// ephemeral, so localStorage starts empty each time, as it does
			// for vibez on linux/amd64, macOS and Windows.
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

			start := time.Now()
			var mu sync.Mutex
			var requests []string
			pg.On("request", func(req playwright.Request) {
				u, err := url.Parse(req.URL())
				if err != nil || !strings.HasSuffix(u.Host, "music.apple.com") {
					return
				}
				mu.Lock()
				requests = append(requests, fmt.Sprintf("%6.0fms %s %s%s", time.Since(start).Seconds()*1000, req.Method(), u.Host, u.Path))
				mu.Unlock()
			})

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
				var r struct {
					MusicKitVersion string            `json:"musickitVersion"`
					Seed            string            `json:"seed"`
					SeedError       *string           `json:"seedError"`
					Steps           []json.RawMessage `json:"steps"`
					Events          []json.RawMessage `json:"events"`
					Errors          json.RawMessage   `json:"errors"`
				}
				if err := json.Unmarshal([]byte(raw), &r); err != nil {
					t.Fatalf("probe returned unparseable JSON: %v\n%s", err, raw)
				}
				dest := filepath.Clean(os.Getenv("VIBEZ_PROBE_OUT"))
				if dest == "." {
					dest = filepath.Join(os.TempDir(), "vibez-probe-163-storefront-"+name+".json")
				}
				var pretty any
				_ = json.Unmarshal([]byte(raw), &pretty)
				out, _ := json.MarshalIndent(pretty, "", "  ")
				//nolint:gosec // G703: dest is an opt-in path from the operator running the probe locally, not untrusted input
				if err := os.WriteFile(dest, out, 0o600); err != nil {
					t.Logf("could not write %s: %v", dest, err)
				} else {
					t.Logf("full probe result written to %s", dest)
				}
				t.Logf("MusicKit %s, seed=%q seedError=%v", r.MusicKitVersion, r.Seed, r.SeedError)
				for _, s := range r.Steps {
					t.Logf("step:  %s", s)
				}
				for _, e := range r.Events {
					t.Logf("event: %s", e)
				}
				t.Logf("errors: %s", r.Errors)
				mu.Lock()
				for _, rq := range requests {
					t.Logf("request: %s", rq)
				}
				mu.Unlock()
			case <-time.After(3 * time.Minute):
				t.Fatal("probe did not report a result within 3m")
			}
		})
	}
}
