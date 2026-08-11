// PROTOTYPE INSTRUMENT — issue #96 (seamless transitions). Not shipped.
//
// Companion to TestProbeGapless. That probe only ever called
// setQueue({songs:[...]}), so it could not answer whether the items: form
// builds a multi-item native queue — which is what decides whether library
// items can join one. See probe_items.html for the questions.
//
// Same launch path and same opt-in as the gapless probe:
//
//	VIBEZ_PROBE=1 VIBEZ_PROBE_IDS=1665303761,1665303762,1665303763 \
//	  go test ./internal/player/cdp -run TestProbeItemsQueue -v -count=1 -timeout 10m
//
// -count=1 matters: without it the Go test cache replays a previous run's
// measurements and identical numbers look like determinism.
//
// Needs an Apple Music session whose library has at least one song, otherwise
// the mixed and library-only phases report themselves as skipped.
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

//go:embed probe_items.html
var probeItemsHTML string

func TestProbeItemsQueue(t *testing.T) {
	if os.Getenv("VIBEZ_PROBE") != "1" {
		t.Skip("set VIBEZ_PROBE=1 to run the #96 items-queue probe (needs a real Apple Music session)")
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
	tmpl, err := template.New("probeItems").Parse(probeItemsHTML)
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

	select {
	case raw := <-resultCh:
		var pretty any
		if err := json.Unmarshal([]byte(raw), &pretty); err != nil {
			t.Fatalf("probe returned unparseable JSON: %v\n%s", err, raw)
		}
		out, _ := json.MarshalIndent(pretty, "", "  ")

		dest := filepath.Clean(os.Getenv("VIBEZ_PROBE_OUT"))
		if dest == "." {
			dest = filepath.Join(os.TempDir(), "vibez-probe-96-items.json")
		}
		//nolint:gosec // G703: dest is an opt-in path from the operator running the probe locally, not untrusted input
		if err := os.WriteFile(dest, out, 0o600); err != nil {
			t.Logf("could not write %s: %v", dest, err)
		} else {
			t.Logf("full probe result written to %s", dest)
		}
		summarizeItems(t, out)
	case <-time.After(8 * time.Minute):
		t.Fatal("probe did not report a result within 8m — check that playback actually started")
	}
}

// summarizeItems prints the facts the #96 design decision turns on, so the
// answer is readable without opening the JSON.
func summarizeItems(t *testing.T, out []byte) {
	t.Helper()
	var r struct {
		MusicKitVersion string `json:"musickitVersion"`
		Storefront      string `json:"storefront"`
		ResolvedCatalog []struct {
			ID        string  `json:"id"`
			Type      string  `json:"type"`
			Name      string  `json:"name"`
			LibraryID *string `json:"libraryId"`
		} `json:"resolvedCatalog"`
		LibrarySongs []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"librarySongs"`
		Phases []struct {
			Name    string `json:"name"`
			Skipped string `json:"skipped"`
			Error   string `json:"error"`

			AfterSetQueue *struct {
				Length    *int     `json:"length"`
				Position  *int     `json:"position"`
				ItemIDs   []string `json:"itemIds"`
				ItemTypes []string `json:"itemTypes"`
			} `json:"afterSetQueue"`
			MultiItemBuilt   *bool     `json:"multiItemBuilt"`
			StartedPlaying   *bool     `json:"startedPlaying"`
			BothItemsPresent *bool     `json:"bothItemsPresent"`
			AdvancedOnItsOwn *bool     `json:"advancedOnItsOwn"`
			SkippedToSecond  *bool     `json:"skippedToSecond"`
			PlayingAfterSkip *bool     `json:"playingAfterSkip"`
			SkipError        string    `json:"skipError"`
			SetQueueThrew    *bool     `json:"setQueueThrew"`
			SetQueueError    string    `json:"setQueueError"`
			LibraryIDInQueue *bool     `json:"libraryIdInQueue"`
			DistinctSrcs     *int      `json:"distinctMediaSrcs"`
			Gap              *probeGap `json:"gap"`
			Note             string    `json:"note"`
			ReachedLibrary   *bool     `json:"reachedLibraryItem"`
			MediaAdvanced    *bool     `json:"mediaActuallyAdvanced"`
			PlayError        string    `json:"playError"`
			SecondID         string    `json:"secondId"`
			NowPlayingID     string    `json:"nowPlayingId"`
		} `json:"phases"`
		Errors []struct {
			Stage string `json:"stage"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Logf("could not summarize: %v", err)
		return
	}

	b := func(p *bool) string {
		if p == nil {
			return "—"
		}
		if *p {
			return "yes"
		}
		return "no"
	}

	t.Logf("MusicKit %s, storefront %s", r.MusicKitVersion, r.Storefront)
	t.Logf("resolved %d catalog song(s), %d library song(s) available",
		len(r.ResolvedCatalog), len(r.LibrarySongs))
	for _, c := range r.ResolvedCatalog {
		lib := "none"
		if c.LibraryID != nil {
			lib = *c.LibraryID
		}
		t.Logf("  catalog %s %q (library copy: %s)", c.ID, c.Name, lib)
	}

	for _, p := range r.Phases {
		t.Logf("── %s", p.Name)
		switch {
		case p.Skipped != "":
			t.Logf("   SKIPPED: %s", p.Skipped)
			continue
		case p.Error != "":
			t.Logf("   ERROR: %s", p.Error)
		}
		if q := p.AfterSetQueue; q != nil {
			ln := "nil"
			if q.Length != nil {
				ln = strconv.Itoa(*q.Length)
			}
			t.Logf("   queue length=%s ids=%v types=%v", ln, q.ItemIDs, q.ItemTypes)
		}
		if p.MultiItemBuilt != nil {
			t.Logf("   multi-item built: %s", b(p.MultiItemBuilt))
		}
		if p.StartedPlaying != nil {
			t.Logf("   started playing: %s", b(p.StartedPlaying))
		}
		if p.BothItemsPresent != nil {
			t.Logf("   both requested items present: %s", b(p.BothItemsPresent))
		}
		if p.AdvancedOnItsOwn != nil {
			t.Logf("   advanced on its own: %s", b(p.AdvancedOnItsOwn))
		}
		if p.Gap != nil {
			srcs := "—"
			if p.DistinctSrcs != nil {
				srcs = strconv.Itoa(*p.DistinctSrcs)
			}
			t.Logf("   boundary gap: %s (distinct media srcs: %s)", gapStr(p.Gap), srcs)
		}
		if p.SkippedToSecond != nil {
			t.Logf("   skipToNextItem reached the library item: %s (playing after: %s)",
				b(p.SkippedToSecond), b(p.PlayingAfterSkip))
		}
		if p.SkipError != "" {
			t.Logf("   skip error: %s", p.SkipError)
		}
		if p.SetQueueThrew != nil {
			t.Logf("   songs: setQueue threw: %s %s", b(p.SetQueueThrew), p.SetQueueError)
		}
		if p.LibraryIDInQueue != nil {
			t.Logf("   library ID present in resulting queue: %s", b(p.LibraryIDInQueue))
		}
		if p.ReachedLibrary != nil {
			t.Logf("   crossed into the library item: %s (now playing %s)", b(p.ReachedLibrary), p.SecondID)
		}
		if p.MediaAdvanced != nil {
			t.Logf("   media actually advanced: %s (now playing %s)", b(p.MediaAdvanced), p.NowPlayingID)
		}
		if p.PlayError != "" {
			t.Logf("   play error: %s", p.PlayError)
		}
		if p.Note != "" {
			t.Logf("   note: %s", p.Note)
		}
	}
	for _, e := range r.Errors {
		t.Logf("error [%s]: %s", e.Stage, e.Error)
	}
}
