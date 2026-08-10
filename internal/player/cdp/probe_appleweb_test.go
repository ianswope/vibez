// PROTOTYPE INSTRUMENT — issue #96 phase G (Apple's own web player). Not shipped.
//
// vmax1g's claim is that the same album plays gapless on Apple's own player.
// Phases A-F established that OUR use of MusicKit.js bottoms out at ~450ms,
// caused by a single media element whose src is swapped at the boundary: the
// next track cannot be decoded or licensed until the current one lets go.
//
// music.apple.com runs MusicKit.js too. So its architecture answers the
// question either way:
//
//	two media elements  -> Apple double-buffers, the mechanism exists, and our
//	                       phase-E design was right but needs their approach to
//	                       the concurrency rule
//	one media element   -> Apple's web player has the same floor we do, and
//	                       vmax1g is comparing against the native app's
//	                       AVQueuePlayer. ~450ms is then the honest web ceiling
//	                       and we can say so on the issue with evidence
//
// Full playback there needs an interactive Apple ID login, which this probe
// deliberately does not attempt — it records how far an anonymous session gets
// and reports that honestly rather than guessing.
//
//	VIBEZ_PROBE_APPLEWEB=1 \
//	  go test -count=1 ./internal/player/cdp -run TestProbeAppleWeb -v -timeout 5m
//
// Knobs: VIBEZ_PROBE_APPLEWEB_URL (page to open, defaults to the DSOTM album),
// VIBEZ_PROBE_APPLEWEB_OUT (result path), VIBEZ_PROBE_WATCH (seconds to watch
// for media elements, default 25).
package cdp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	playwright "github.com/mxschmitt/playwright-go"
)

func TestProbeAppleWeb(t *testing.T) {
	if os.Getenv("VIBEZ_PROBE_APPLEWEB") != "1" {
		t.Skip("set VIBEZ_PROBE_APPLEWEB=1 to inspect Apple's own web player")
	}

	url := os.Getenv("VIBEZ_PROBE_APPLEWEB_URL")
	if url == "" {
		url = "https://music.apple.com/us/album/the-dark-side-of-the-moon-50th-anniversary/1665303755"
	}
	watchSec := envInt("VIBEZ_PROBE_WATCH", 25, 5)

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
	pg.On("pageerror", func(err error) { t.Logf("page error: %v", err) })

	t.Logf("opening %s", url)
	if _, err := pg.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(60000),
	}); err != nil {
		t.Fatalf("goto: %v", err)
	}

	// MusicKit is loaded asynchronously by their bundle; give it room.
	if _, err := pg.WaitForFunction(`() => !!window.MusicKit`, playwright.PageWaitForFunctionOptions{
		Timeout: playwright.Float(60000),
	}); err != nil {
		t.Logf("window.MusicKit never appeared: %v", err)
	}

	// Try to start playback. Anonymous sessions get a preview or a login wall;
	// either way what matters is how many media elements their player builds.
	clicked, _ := pg.Evaluate(`() => {
		const sels = ['button[aria-label*="Play" i]', 'button[data-testid*="play" i]', '.play-button', 'amp-playback-controls-play'];
		for (const s of sels) {
			const el = document.querySelector(s);
			if (el) { el.click(); return s; }
		}
		return null;
	}`)
	t.Logf("play control clicked: %v", clicked)

	// Watch: media elements can be created lazily, so sample rather than peek.
	watch, err := pg.Evaluate(`async (secs) => {
		const out = { samples: [], maxElements: 0, distinctSrcs: [], musickit: {}, errors: [] };
		try {
			out.musickit.version = window.MusicKit?.version ?? null;
			let inst = null;
			try { inst = window.MusicKit?.getInstance?.(); } catch (e) { out.errors.push('getInstance: ' + e.message); }
			out.musickit.hasInstance = !!inst;
			if (inst) {
				out.musickit.isAuthorized = !!inst.isAuthorized;
				out.musickit.storefront = inst.storefrontId ?? null;
				out.musickit.queueLength = inst.queue?.length ?? null;
				const fns = [];
				for (let p = inst; p && p !== Object.prototype; p = Object.getPrototypeOf(p)) {
					for (const k of Object.getOwnPropertyNames(p)) {
						const d = Object.getOwnPropertyDescriptor(p, k);
						if (d && typeof d.value === 'function') fns.push(k);
					}
				}
				out.musickit.fnCount = new Set(fns).size;
				out.musickit.hasPreloadApi = fns.some(f => /pre(pare|load|fetch)/i.test(f));
			}
		} catch (e) { out.errors.push('musickit: ' + e.message); }

		const seen = new Set();
		const t0 = performance.now();
		while (performance.now() - t0 < secs * 1000) {
			const els = Array.from(document.querySelectorAll('audio,video'));
			out.maxElements = Math.max(out.maxElements, els.length);
			for (const el of els) if (el.currentSrc) seen.add(el.currentSrc.slice(0, 60));
			out.samples.push({
				t: Math.round(performance.now() - t0),
				n: els.length,
				els: els.map(el => ({
					tag: el.tagName,
					ct: +(el.currentTime || 0).toFixed(2),
					paused: !!el.paused,
					ready: el.readyState,
					muted: !!el.muted,
					hasSrc: !!(el.currentSrc || el.src),
				})),
			});
			await new Promise(r => setTimeout(r, 500));
		}
		out.distinctSrcs = [...seen];
		return out;
	}`, watchSec)
	if err != nil {
		t.Fatalf("evaluate watch: %v", err)
	}

	out, _ := json.MarshalIndent(watch, "", "  ")
	dest := filepath.Clean(os.Getenv("VIBEZ_PROBE_APPLEWEB_OUT"))
	if dest == "." {
		dest = filepath.Join(os.TempDir(), "vibez-probe-96-appleweb.json")
	}
	//nolint:gosec // G703: dest is an opt-in path from the operator running the probe locally, not untrusted input
	if err := os.WriteFile(dest, out, 0o600); err != nil {
		t.Logf("could not write %s: %v", dest, err)
	} else {
		t.Logf("full result written to %s", dest)
	}

	var r struct {
		MaxElements  int      `json:"maxElements"`
		DistinctSrcs []string `json:"distinctSrcs"`
		Errors       []string `json:"errors"`
		MusicKit     struct {
			Version       string `json:"version"`
			HasInstance   bool   `json:"hasInstance"`
			IsAuthorized  bool   `json:"isAuthorized"`
			Storefront    string `json:"storefront"`
			FnCount       int    `json:"fnCount"`
			HasPreloadAPI bool   `json:"hasPreloadApi"`
		} `json:"musickit"`
		Samples []struct {
			T   int `json:"t"`
			N   int `json:"n"`
			Els []struct {
				Tag    string  `json:"tag"`
				Ct     float64 `json:"ct"`
				Paused bool    `json:"paused"`
				Ready  int     `json:"ready"`
				HasSrc bool    `json:"hasSrc"`
			} `json:"els"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	t.Log("──────── #96 phase G: Apple's own web player ────────")
	t.Logf("MusicKit version : %s   (ours: 3.2526.0-prerelease.x)", r.MusicKit.Version)
	t.Logf("instance present : %v   authorized: %v   storefront: %q", r.MusicKit.HasInstance, r.MusicKit.IsAuthorized, r.MusicKit.Storefront)
	t.Logf("instance fns     : %d   exposes a preload/prepare API: %v", r.MusicKit.FnCount, r.MusicKit.HasPreloadAPI)
	t.Logf("MAX simultaneous media elements: %d   <- the architectural verdict", r.MaxElements)
	t.Logf("distinct media srcs seen: %d", len(r.DistinctSrcs))
	for _, s := range r.DistinctSrcs {
		t.Logf("   %s", s)
	}
	// Print only samples where the element picture changed.
	prev := -1
	for _, s := range r.Samples {
		if s.N == prev {
			continue
		}
		prev = s.N
		t.Logf("  t=%5dms  elements=%d", s.T, s.N)
		for _, e := range s.Els {
			t.Logf("      %s ct=%.2f paused=%v ready=%d hasSrc=%v", e.Tag, e.Ct, e.Paused, e.Ready, e.HasSrc)
		}
	}
	for _, e := range r.Errors {
		t.Logf("ERROR %s", e)
	}
	t.Log("─────────────────────────────────────────────────────")
}
