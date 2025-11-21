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

// PURE yt-dlp search
func searchYouTube(query string) ([]cache.MusicTrack, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty search query")
	}

	yt := NewYouTubeData(query)
	isURL := yt.IsValid()

	args := []string{
		"-j",
		"--no-warnings",
		"--no-playlist",
	}

	if isURL {
		args = append(args, yt.normalizeYouTubeURL(query))
	} else {
		args = append(args, "ytsearch5:"+query)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	out, err := cmd.CombinedOutput()

	scanner := bufio.NewScanner(bytes.NewReader(out))
	var tracks []cache.MusicTrack

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "{") {
			continue
		}

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

		tracks = append(tracks, cache.MusicTrack{
			ID:       e.ID,
			Name:     e.Title,
			URL:      url,
			Cover:    thumb,
			Duration: int(e.Duration),
			Platform: "youtube",
		})
	}

	if len(tracks) > 0 {
		return tracks, nil
	}

	if err != nil {
		return nil, fmt.Errorf("yt-dlp error: %v | %s", err, string(out))
	}

	return nil, fmt.Errorf("no search results found")
}
