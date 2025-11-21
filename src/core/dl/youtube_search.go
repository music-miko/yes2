/*
 * TgMusicBot - Telegram Music Bot
 *  Copyright (c) 2025 Ashok Shau
 *
 *  Licensed under GNU GPL v3
 *  See https://github.com/AshokShau/TgMusicBot
 */

package dl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"ashokshau/tgmusic/src/config"
	"ashokshau/tgmusic/src/core/cache"
)

// ytDlpEntry represents the subset of fields we need from yt-dlp JSON output.
type ytDlpEntry struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Webpage   string  `json:"webpage_url"`
	Duration  float64 `json:"duration"`  // seconds
	Thumbnail string  `json:"thumbnail"` // best thumbnail url
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

// searchYouTube uses yt-dlp instead of HTML scraping to search YouTube.
//
// It runs: yt-dlp -j --no-playlist "ytsearch5:<query>"
// and converts each JSON line into a cache.MusicTrack.
//
// It also reuses cookies / proxy configuration from YouTubeData to reduce
// the chance of being blocked by YouTube.
func searchYouTube(query string) ([]cache.MusicTrack, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("empty search query")
	}

	// Use the same settings as YouTube downloads (cookies / proxy)
	y := NewYouTubeData(q)

	args := []string{
		"-j",
		"--no-playlist",
	}

	// Reuse cookie / proxy logic similar to BuildYtdlpParams
	if cookieFile := y.getCookieFile(); cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	} else if config.Conf.Proxy != "" {
		args = append(args, "--proxy", config.Conf.Proxy)
	}

	args = append(args, "ytsearch5:"+q)

	// Context with timeout so search can't hang forever.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)

	// We use CombinedOutput so that if yt-dlp returns non-zero,
	// we can inspect stderr for debugging. We'll parse only JSON lines.
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Don't immediately fail; we'll still try to parse whatever JSON we got.
		// If no valid entries are found, we will surface this error later.
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	var tracks []cache.MusicTrack

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// yt-dlp with -j prints each result as its own JSON object per line.
		// Non-JSON lines (logs, warnings) will fail to unmarshal and be skipped.
		var e ytDlpEntry
		if jsonErr := json.Unmarshal([]byte(line), &e); jsonErr != nil {
			continue
		}

		if e.ID == "" {
			continue
		}

		thumb := e.Thumbnail
		if thumb == "" && len(e.Thumbnails) > 0 {
			thumb = e.Thumbnails[len(e.Thumbnails)-1].URL
		}

		url := e.Webpage
		if url == "" {
			url = "https://www.youtube.com/watch?v=" + e.ID
		}

		duration := int(e.Duration)

		tracks = append(tracks, cache.MusicTrack{
			URL:      url,
			Name:     e.Title,
			ID:       e.ID,
			Cover:    thumb,
			Duration: duration,
			Platform: "youtube",
		})
	}

	// If we successfully parsed some tracks, treat search as successful even if
	// yt-dlp returned a non-zero exit code.
	if len(tracks) > 0 {
		return tracks, nil
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return nil, fmt.Errorf("failed to read yt-dlp output: %w", scanErr)
	}

	// At this point we have no tracks and possibly an underlying error.
	if err != nil {
		// Include a short snippet of yt-dlp output for easier debugging.
		snippet := string(out)
		if len(snippet) > 300 {
			snippet = snippet[:300] + "..."
		}
		return nil, fmt.Errorf("yt-dlp search failed: %v | output: %s", err, snippet)
	}

	return nil, fmt.Errorf("no search results found")
}
