package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/simone-vibes/vibez/internal/config"
	"github.com/simone-vibes/vibez/internal/provider/apple"
)

func main() {
	songIDFlag := flag.String("song", "", "Apple Music song ID (optional; if empty, searches for a song)")
	flag.Parse()

	cfg, err := config.Load("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	if cfg.AppleDeveloperToken == "" || cfg.AppleUserToken == "" {
		fmt.Fprintf(os.Stderr, "Apple Developer Token or User Token is missing in config\n")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	songID := *songIDFlag
	songTitle := ""
	if songID == "" {
		fmt.Println("Searching for a test track...")
		provider := apple.New(cfg)
		res, err := provider.Search(ctx, "Daft Punk Get Lucky")
		if err != nil || len(res.Tracks) == 0 {
			fmt.Fprintf(os.Stderr, "Search failed or returned no tracks: %v\n", err)
			os.Exit(1)
		}
		track := res.Tracks[0]
		songID = track.ID
		songTitle = fmt.Sprintf("%s - %s", track.Artist, track.Title)
		fmt.Printf("Using track: %s (ID: %s)\n\n", songTitle, songID)
	}

	client := &http.Client{Timeout: 15 * time.Second}

	// 1. Test MZPlay webPlayback endpoint
	fmt.Println("=== Probe 1: MZPlay.woa/wa/webPlayback ===")
	mzURL := "https://play.itunes.apple.com/WebObjects/MZPlay.woa/wa/webPlayback"
	reqBody, _ := json.Marshal(map[string]any{
		"salableAdamId": songID,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mzURL, bytes.NewReader(reqBody))
	if err != nil {
		fmt.Printf("Create request error: %v\n", err)
	} else {
		req.Header.Set("Authorization", "Bearer "+cfg.AppleDeveloperToken)
		req.Header.Set("X-Apple-Music-User-Token", cfg.AppleUserToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Origin", "https://music.apple.com")
		req.Header.Set("Referer", "https://music.apple.com/")
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("MZPlay request failed: %v\n", err)
		} else {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			fmt.Printf("Status: %d %s\n", resp.StatusCode, resp.Status)
			if resp.StatusCode == http.StatusOK {
				var parsed map[string]any
				if err := json.Unmarshal(body, &parsed); err == nil {
					songList, ok := parsed["songList"].([]any)
					if ok && len(songList) > 0 {
						firstSong, _ := songList[0].(map[string]any)
						fmt.Printf("✓ Success! Received songList item:\n")
						for k, v := range firstSong {
							switch val := v.(type) {
							case string:
								if strings.HasPrefix(val, "http") {
									fmt.Printf("  %s: %s\n", k, val)
								} else {
									fmt.Printf("  %s: %v\n", k, val)
								}
							case []any:
								fmt.Printf("  %s: [%d items]\n", k, len(val))
								for i, item := range val {
									if itemMap, ok := item.(map[string]any); ok {
										fmt.Printf("    Item %d keys: ", i)
										for ik, iv := range itemMap {
											if s, ok := iv.(string); ok && strings.HasPrefix(s, "http") {
												fmt.Printf("%s=%s ", ik, s)
											} else {
												fmt.Printf("%s=%v ", ik, iv)
											}
										}
										fmt.Println()
									}
								}
							default:
								fmt.Printf("  %s: %v\n", k, v)
							}
						}
					} else {
						fmt.Printf("Response json (no songList): %s\n", string(body))
					}
				} else {
					fmt.Printf("Response body: %s\n", string(body))
				}
			} else {
				fmt.Printf("Response: %s\n", string(body))
			}
		}
	}

	// 3. Test License Server (acquireWebPlaybackLicense)
	fmt.Println("\n=== Probe 3: acquireWebPlaybackLicense ===")
	licenseURL := "https://play.itunes.apple.com/WebObjects/MZPlay.woa/wa/acquireWebPlaybackLicense"
	probeChallenge := map[string]any{
		"challenge":      "dGVzdGNoYWxsZW5nZQ==", // "testchallenge" in base64
		"uri":            "data:;base64,AAAAACTJBz4AHX4C2PDAYg==",
		"key-system":     "com.widevine.alpha",
		"adamId":         songID,
		"isLibrary":      false,
		"user-initiated": true,
	}
	licBody, _ := json.Marshal(probeChallenge)
	req3, err := http.NewRequestWithContext(ctx, http.MethodPost, licenseURL, bytes.NewReader(licBody))
	if err != nil {
		fmt.Printf("Create license request error: %v\n", err)
	} else {
		req3.Header.Set("Authorization", "Bearer "+cfg.AppleDeveloperToken)
		req3.Header.Set("X-Apple-Music-User-Token", cfg.AppleUserToken)
		req3.Header.Set("Content-Type", "application/json")
		req3.Header.Set("Accept", "application/json")
		req3.Header.Set("X-Apple-Renewal", "true")
		req3.Header.Set("Origin", "https://music.apple.com")
		req3.Header.Set("Referer", "https://music.apple.com/")

		resp3, err := client.Do(req3)
		if err != nil {
			fmt.Printf("License request error: %v\n", err)
		} else {
			defer resp3.Body.Close()
			body3, _ := io.ReadAll(resp3.Body)
			fmt.Printf("Status: %d %s\n", resp3.StatusCode, resp3.Status)
			fmt.Printf("Response: %s\n", string(body3))
		}
	}
}
