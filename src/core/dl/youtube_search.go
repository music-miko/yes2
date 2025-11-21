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

	"ashokshau/tgmusic/src/core/cache"
)

// Minimal yt-dlp JSON structure we need
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

// --- NEW SEARCH METHOD ---
// 100% Google-block-proof YouTube search using yt-dlp
//
// Command:
//    yt-dlp -j --no-playlist "ytsearch5:<query>"
//
// yt-dlp returns multiple JSON objects (one per line)
func searchYouTube(query string) ([]cache.MusicTrack, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("empty search query")
	}

	// Prevent yt-dlp from hanging forever
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "yt-dlp",
		"-j",
		"--no-playlist",
		"ytsearch5:"+q,
	)

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("yt-dlp search timed out")
		}
		return nil, fmt.Errorf("yt-dlp search error: %v", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	var results []cache.MusicTrack

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var item ytDlpEntry
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}

		if item.ID == "" {
			continue
		}

		thumb := item.Thumbnail
		if thumb == "" && len(item.Thumbnails) > 0 {
			thumb = item.Thumbnails[len(item.Thumbnails)-1].URL
		}

		url := item.Webpage
		if url == "" {
			url = "https://www.youtube.com/watch?v=" + item.ID
		}

		results = append(results, cache.MusicTrack{
			ID:       item.ID,
			Name:     item.Title,
			URL:      url,
			Cover:    thumb,
			Duration: int(item.Duration),
			Platform: "youtube",
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read yt-dlp output: %v", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("no results found")
	}

	return results, nil
}
