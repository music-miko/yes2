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

// ytDlpEntry is the subset of fields we care about from yt-dlp -j output.
type ytDlpEntry struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Webpage   string  `json:"webpage_url"`
	Duration  float64 `json:"duration"`
	Thumbnail string  `json:"thumbnail"`
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

// searchYouTube performs YouTube search / metadata lookup using yt-dlp only.
//
// - For plain text queries: yt-dlp -j --no-playlist "ytsearch5:<query>"
// - For YouTube URLs / IDs: yt-dlp -j --no-playlist "<normalized_url>"
//
// All old HTML scraping / Google /sorry logic is completely removed.
func searchYouTube(query string) ([]cache.MusicTrack, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("empty search query")
	}

	// Build a YouTubeData helper so we can reuse URL normalization and cookies.
	yt := NewYouTubeData(q)
	isURL := yt.IsValid()

	// 1) Base args for yt-dlp
	args := []string{
		"-j",
		"--no-warnings",
		"--no-playlist",
	}

	// 2) Cookies / proxy (same idea as in BuildYtdlpParams)
	if cookieFile := yt.getCookieFile(); cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	} else if config.Conf.Proxy != "" {
		args = append(args, "--proxy", config.Conf.Proxy)
	}

	// 3) Target: either direct URL or search mode
	if isURL {
		// Normalize shorts / youtu.be to standard watch URL
		args = append(args, yt.normalizeYouTubeURL(q))
	} else {
		// Text search
		args = append(args, "ytsearch5:"+q)
	}

	// 4) Run yt-dlp with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	out, err := cmd.CombinedOutput()

	// 5) Parse JSON lines from stdout/stderr
	scanner := bufio.NewScanner(bytes.NewReader(out))
	var tracks []cache.MusicTrack

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			// ignore non-JSON logs from yt-dlp
			continue
		}

		var e ytDlpEntry
		if jsonErr := json.Unmarshal([]byte(line), &e); jsonErr != nil {
			// ignore malformed lines
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

		tracks = append(tracks, cache.MusicTrack{
			ID:       e.ID,
			Name:     e.Title,
			URL:      url,
			Cover:    thumb,
			Duration: int(e.Duration),
			Platform: "youtube",
		})
	}

	// If we parsed some tracks, treat as success even if yt-dlp exit code was non-zero.
	if len(tracks) > 0 {
		return tracks, nil
	}

	// No tracks parsed
	if scanErr := scanner.Err(); scanErr != nil {
		return nil, fmt.Errorf("failed to read yt-dlp output: %w", scanErr)
	}

	// If yt-dlp errored and gave us no JSON to parse, surface a short error.
	if err != nil {
		s := string(out)
		if len(s) > 300 {
			s = s[:300] + "..."
		}
		return nil, fmt.Errorf("yt-dlp search failed: %v | output: %s", err, s)
	}

	return nil, fmt.Errorf("no search results found")
}
