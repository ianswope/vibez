// PROTOTYPE INSTRUMENT — issue #96 (seamless transitions). Not shipped.
//
// Repeats each boundary design several times and reports every sample. The
// single-shot probes disagreed with themselves on re-run (the same
// catalog/catalog native arm gave 15.9ms/1 src, then 349.9ms/2 srcs), so no
// number from them should be quoted without a distribution behind it.
//
//	VIBEZ_PROBE=1 VIBEZ_PROBE_REPEAT=5 \
//	  VIBEZ_PROBE_IDS=1665303761,1665303762,1665303763 \
//	  go test ./internal/player/cdp -run TestProbeGapRepeat -v -count=1 -timeout 40m
//
// -count=1 matters: without it the Go test cache replays a previous run and
// identical numbers look like determinism.
//
// Each sample plays a real track to its end, so budget roughly 15s per sample:
// four arms at five repeats is about five minutes of playback.
package cdp

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	playwright "github.com/mxschmitt/playwright-go"

	"github.com/simone-vibes/vibez/internal/config"
)

//go:embed probe_gaprepeat.html
var probeGapRepeatHTML string

func TestProbeGapRepeat(t *testing.T) {
	if os.Getenv("VIBEZ_PROBE") != "1" {
		t.Skip("set VIBEZ_PROBE=1 to run the #96 repeat probe (needs a real Apple Music session)")
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

	repeats := 5
	if v := os.Getenv("VIBEZ_PROBE_REPEAT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			repeats = n
		}
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
	tmpl, err := template.New("probeRepeat").Parse(probeGapRepeatHTML)
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
		"Repeats":        repeats,
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

	if _, err := pg.Goto(fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)); err != nil {
		t.Fatalf("goto: %v", err)
	}

	// Roughly 15s of real playback per sample, plus setup.
	budget := time.Duration(repeats*4*30+180) * time.Second
	select {
	case raw := <-resultCh:
		var pretty any
		if err := json.Unmarshal([]byte(raw), &pretty); err != nil {
			t.Fatalf("probe returned unparseable JSON: %v\n%s", err, raw)
		}
		out, _ := json.MarshalIndent(pretty, "", "  ")

		dest := filepath.Clean(os.Getenv("VIBEZ_PROBE_OUT"))
		if dest == "." {
			dest = filepath.Join(os.TempDir(), "vibez-probe-96-repeat.json")
		}
		//nolint:gosec // G703: dest is an opt-in path from the operator running the probe locally, not untrusted input
		if err := os.WriteFile(dest, out, 0o600); err != nil {
			t.Logf("could not write %s: %v", dest, err)
		} else {
			t.Logf("full probe result written to %s", dest)
		}
		summarizeRepeat(t, out)
	case <-time.After(budget):
		t.Fatalf("probe did not report a result within %s — check that playback actually started", budget)
	}
}

func summarizeRepeat(t *testing.T, out []byte) {
	t.Helper()
	var r struct {
		MusicKitVersion   string `json:"musickitVersion"`
		MixedSecondItemID string `json:"mixedSecondItemId"`
		Catalog           []struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			LibraryID *string `json:"libraryId"`
		} `json:"catalog"`
		Arms []struct {
			Name      string `json:"name"`
			ItemCount int    `json:"itemCount"`
		} `json:"arms"`
		Samples []struct {
			Arm          string   `json:"arm"`
			Rep          int      `json:"rep"`
			PosInRep     int      `json:"posInRep"`
			GapMs        *float64 `json:"gapMs"`
			DistinctSrcs *int     `json:"distinctMediaSrcs"`
			Advanced     *bool    `json:"advanced"`
			Error        string   `json:"error"`
		} `json:"samples"`
		Errors []struct {
			Stage string `json:"stage"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Logf("could not summarize: %v", err)
		return
	}

	t.Logf("MusicKit %s", r.MusicKitVersion)
	for _, c := range r.Catalog {
		lib := "none"
		if c.LibraryID != nil {
			lib = *c.LibraryID
		}
		t.Logf("  %s %q (library copy: %s)", c.ID, c.Name, lib)
	}
	if r.MixedSecondItemID != "" {
		t.Logf("  mixed arm's second item: %s", r.MixedSecondItemID)
	}

	// Preserve first-seen arm order rather than sorting alphabetically.
	var order []string
	byArm := map[string][]float64{}
	srcs := map[string]map[int]int{}
	fails := map[string]int{}
	total := map[string]int{}
	for _, s := range r.Samples {
		if _, seen := total[s.Arm]; !seen {
			order = append(order, s.Arm)
			srcs[s.Arm] = map[int]int{}
		}
		total[s.Arm]++
		if s.Error != "" || s.GapMs == nil {
			fails[s.Arm]++
			continue
		}
		byArm[s.Arm] = append(byArm[s.Arm], *s.GapMs)
		if s.DistinctSrcs != nil {
			srcs[s.Arm][*s.DistinctSrcs]++
		}
	}

	t.Logf("")
	t.Logf("%-20s %5s %9s %9s %9s %9s  %s", "arm", "n", "min", "median", "max", "spread", "distinct media srcs")
	for _, arm := range order {
		v := append([]float64(nil), byArm[arm]...)
		if len(v) == 0 {
			t.Logf("%-20s %5d  no measured samples (%d unmeasured)", arm, 0, fails[arm])
			continue
		}
		sort.Float64s(v)
		med := v[len(v)/2]
		if len(v)%2 == 0 {
			med = (v[len(v)/2-1] + v[len(v)/2]) / 2
		}
		var parts []string
		for _, n := range []int{1, 2, 3} {
			if c := srcs[arm][n]; c > 0 {
				parts = append(parts, fmt.Sprintf("%dsrc×%d", n, c))
			}
		}
		t.Logf("%-20s %5d %8.1fms %8.1fms %8.1fms %8.1fms  %s",
			arm, len(v), v[0], med, v[len(v)-1], v[len(v)-1]-v[0], strings.Join(parts, " "))
		if fails[arm] > 0 {
			t.Logf("%-20s       (%d of %d samples unmeasured)", "", fails[arm], total[arm])
		}
	}

	// Position within the rep is the confound rotation is meant to neutralise:
	// an arm that always ran last would benefit from whatever the earlier arms
	// warmed. If these rows differ materially, position still matters.
	t.Logf("")
	byPos := map[int][]float64{}
	posSrcs := map[int]map[int]int{}
	for _, s := range r.Samples {
		if s.GapMs == nil {
			continue
		}
		byPos[s.PosInRep] = append(byPos[s.PosInRep], *s.GapMs)
		if posSrcs[s.PosInRep] == nil {
			posSrcs[s.PosInRep] = map[int]int{}
		}
		if s.DistinctSrcs != nil {
			posSrcs[s.PosInRep][*s.DistinctSrcs]++
		}
	}
	var positions []int
	for p := range byPos {
		positions = append(positions, p)
	}
	sort.Ints(positions)
	t.Logf("position-in-rep effect (across all arms):")
	for _, p := range positions {
		v := append([]float64(nil), byPos[p]...)
		sort.Float64s(v)
		med := v[len(v)/2]
		if len(v)%2 == 0 {
			med = (v[len(v)/2-1] + v[len(v)/2]) / 2
		}
		var parts []string
		for _, n := range []int{1, 2, 3} {
			if c := posSrcs[p][n]; c > 0 {
				parts = append(parts, fmt.Sprintf("%dsrc×%d", n, c))
			}
		}
		t.Logf("  pos %d: n=%d min=%.1fms median=%.1fms max=%.1fms  %s",
			p, len(v), v[0], med, v[len(v)-1], strings.Join(parts, " "))
	}

	t.Logf("")
	for _, arm := range order {
		var raw []string
		for _, s := range r.Samples {
			if s.Arm != arm {
				continue
			}
			switch {
			case s.Error != "":
				raw = append(raw, "err")
			case s.GapMs == nil:
				raw = append(raw, "unmeasured")
			default:
				raw = append(raw, fmt.Sprintf("%.1f", *s.GapMs))
			}
		}
		t.Logf("%-20s samples: %s", arm, strings.Join(raw, ", "))
	}
	for _, e := range r.Errors {
		t.Logf("error [%s]: %s", e.Stage, e.Error)
	}
}
