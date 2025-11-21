package dl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"ashokshau/tgmusic/src/config"
	"ashokshau/tgmusic/src/core/cache"
)

// -------------------------------
// STRUCTS
// -------------------------------

type apiSearchResponse struct {
	Results []cache.MusicTrack `json:"results"`
}

// Used for yt-dlp parsing
type ytDlpEntry struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Webpage   string   `json:"webpage_url"`
	Duration  float64  `json:"duration"`
	Thumbnail string   `json:"thumbnail"`
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

// -------------------------------
// 1) SEARCH USING EXTERNAL API
// -------------------------------

func searchViaAPI(query string) ([]cache.MusicTrack, error) {
	apiURL := strings.TrimRight(config.Conf.ApiUrl, "/")
	apiKey := config.Conf.ApiKey
	if apiURL == "" || apiKey == "" {
		return nil, fmt.Errorf("api disabled")
	}

	endpoint := fmt.Sprintf("%s/search?query=%s&api=%s", apiURL, url.QueryEscape(query), apiKey)

	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("User-Agent", "ArcBots-Search")

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("api returned status %d", resp.StatusCode)
	}

	var result apiSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid api response: %v", err)
	}

	if len(result.Results) == 0 {
		return nil, fmt.Errorf("api returned no results")
	}

	return result.Results, nil
}

// -------------------------------
// 2) SEARCH USING YT-DLP
// -------------------------------

func searchViaYtDlp(query string) ([]cache.MusicTrack, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	args := []string{
		"-j",
		"--no-warnings",
		"--no-playlist",
		"ytsearch5:" + query,
	}

	// Cookie or Proxy
	if len(config.Conf.CookiesPath) > 0 {
		args = append(args, "--cookies", config.Conf.CookiesPath[0])
	} else if config.Conf.Proxy != "" {
		args = append(args, "--proxy", config.Conf.Proxy)
	}

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	out, err := cmd.CombinedOutput()

	scanner := bufio.NewScanner(bytes.NewReader(out))
	var tracks []cache.MusicTrack

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}

		var v ytDlpEntry
		if jsonErr := json.Unmarshal([]byte(line), &v); jsonErr != nil {
			continue
		}

		if v.ID == "" {
			continue
		}

		thumb := v.Thumbnail
		if thumb == "" && len(v.Thumbnails) > 0 {
			thumb = v.Thumbnails[len(v.Thumbnails)-1].URL
		}

		url := v.Webpage
		if url == "" {
			url = "https://www.youtube.com/watch?v=" + v.ID
		}

		tracks = append(tracks, cache.MusicTrack{
			ID:       v.ID,
			Name:     v.Title,
			URL:      url,
			Cover:    thumb,
			Duration: int(v.Duration),
			Platform: "youtube",
		})
	}

	if len(tracks) > 0 {
		return tracks, nil
	}

	if err != nil {
		return nil, fmt.Errorf("yt-dlp error: %v | %s", err, string(out))
	}

	return nil, fmt.Errorf("no yt-dlp results")
}

// -------------------------------
// MASTER SEARCH (API FIRST, YTDLP FALLBACK)
// -------------------------------

func searchYouTube(query string) ([]cache.MusicTrack, error) {
	// 1) Try API
	results, apiErr := searchViaAPI(query)
	if apiErr == nil && len(results) > 0 {
		return results, nil
	}

	// 2) Fallback → yt-dlp
	return searchViaYtDlp(query)
}
