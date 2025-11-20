/*
 * TgMusicBot - Telegram Music Bot
 *  Copyright (c) 2025 Ashok Shau
 *
 *  Licensed under GNU GPL v3
 *  See https://github.com/AshokShau/TgMusicBot
 */

package dl

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"ashokshau/tgmusic/src/config"
	"ashokshau/tgmusic/src/core/cache"
)

// songAPIResponse matches your Python API's JSON for both /song and /video.
type songAPIResponse struct {
	Status  string `json:"status"`
	Link    string `json:"link"`
	Format  string `json:"format"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

// YouTubeData provides an interface for fetching track and playlist information from YouTube.
type YouTubeData struct {
	Query    string
	ApiUrl   string
	APIKey   string
	Patterns map[string]*regexp.Regexp
}

var youtubePatterns = map[string]*regexp.Regexp{
	"youtube":   regexp.MustCompile(`^(?:https?://)?(?:www\.)?youtube\.com/watch\?v=([\w-]{11})(?:[&#?].*)?$`),
	"youtu_be":  regexp.MustCompile(`^(?:https?://)?(?:www\.)?youtu\.be/([\w-]{11})(?:[?#].*)?$`),
	"yt_shorts": regexp.MustCompile(`^(?:https?://)?(?:www\.)?youtube\.com/shorts/([\w-]{11})(?:[?#].*)?$`),
}

// NewYouTubeData initializes a YouTubeData instance with pre-compiled regex patterns and a cleaned query.
func NewYouTubeData(query string) *YouTubeData {
	return &YouTubeData{
		Query:    clearQuery(query),
		ApiUrl:   strings.TrimRight(config.Conf.ApiUrl, "/"),
		APIKey:   config.Conf.ApiKey,
		Patterns: youtubePatterns,
	}
}

// clearQuery removes extraneous URL parameters and fragments from a given query string.
func clearQuery(query string) string {
	query = strings.SplitN(query, "#", 2)[0]
	query = strings.SplitN(query, "&", 2)[0]
	return strings.TrimSpace(query)
}

// normalizeYouTubeURL converts various YouTube URL formats (e.g., youtu.be, shorts) into a standard watch URL.
func (y *YouTubeData) normalizeYouTubeURL(url string) string {
	var videoID string
	switch {
	case strings.Contains(url, "youtu.be/"):
		parts := strings.SplitN(strings.SplitN(url, "youtu.be/", 2)[1], "?", 2)
		videoID = strings.SplitN(parts[0], "#", 2)[0]
	case strings.Contains(url, "youtube.com/shorts/"):
		parts := strings.SplitN(strings.SplitN(url, "youtube.com/shorts/", 2)[1], "?", 2)
		videoID = strings.SplitN(parts[0], "#", 2)[0]
	default:
		return url
	}
	return "https://www.youtube.com/watch?v=" + videoID
}

// extractVideoID parses a YouTube URL and extracts the video ID.
func (y *YouTubeData) extractVideoID(url string) string {
	url = y.normalizeYouTubeURL(url)
	for _, pattern := range y.Patterns {
		if match := pattern.FindStringSubmatch(url); len(match) > 1 {
			return match[1]
		}
	}
	return ""
}

// IsValid checks if the query string matches any of the known YouTube URL patterns.
func (y *YouTubeData) IsValid() bool {
	if y.Query == "" {
		log.Println("The query or patterns are empty.")
		return false
	}
	for _, pattern := range y.Patterns {
		if pattern.MatchString(y.Query) {
			return true
		}
	}
	return false
}

// GetInfo retrieves metadata for a track from YouTube.
// It returns a PlatformTracks object or an error if the information cannot be fetched.
func (y *YouTubeData) GetInfo(ctx context.Context) (cache.PlatformTracks, error) {
	if !y.IsValid() {
		return cache.PlatformTracks{}, errors.New("the provided URL is invalid or the platform is not supported")
	}

	y.Query = y.normalizeYouTubeURL(y.Query)
	videoID := y.extractVideoID(y.Query)
	if videoID == "" {
		return cache.PlatformTracks{}, errors.New("unable to extract the video ID")
	}

	tracks, err := searchYouTube(y.Query)
	if err != nil {
		return cache.PlatformTracks{}, err
	}

	for _, track := range tracks {
		if track.ID == videoID {
			return cache.PlatformTracks{Results: []cache.MusicTrack{track}}, nil
		}
	}

	return cache.PlatformTracks{}, errors.New("no video results were found")
}

// Search performs a search for a track on YouTube.
// It accepts a context for handling timeouts and cancellations, and returns a PlatformTracks object or an error.
func (y *YouTubeData) Search(ctx context.Context) (cache.PlatformTracks, error) {
	tracks, err := searchYouTube(y.Query)
	if err != nil {
		return cache.PlatformTracks{}, err
	}
	if len(tracks) == 0 {
		return cache.PlatformTracks{}, errors.New("no video results were found")
	}
	return cache.PlatformTracks{Results: tracks}, nil
}

// GetTrack retrieves detailed information for a single track.
// It returns a TrackInfo object or an error if the track cannot be found.
func (y *YouTubeData) GetTrack(ctx context.Context) (cache.TrackInfo, error) {
	if y.Query == "" {
		return cache.TrackInfo{}, errors.New("the query is empty")
	}
	if !y.IsValid() {
		return cache.TrackInfo{}, errors.New("the provided URL is invalid or the platform is not supported")
	}

	// Try external API first (if configured)
	if y.ApiUrl != "" && y.APIKey != "" {
		if trackInfo, err := NewApiData(y.Query).GetTrack(ctx); err == nil {
			return trackInfo, nil
		}
	}

	getInfo, err := y.GetInfo(ctx)
	if err != nil {
		return cache.TrackInfo{}, err
	}
	if len(getInfo.Results) == 0 {
		return cache.TrackInfo{}, errors.New("no video results were found")
	}

	track := getInfo.Results[0]
	trackInfo := cache.TrackInfo{
		URL:      track.URL,
		CdnURL:   "None",
		Key:      "None",
		Name:     track.Name,
		Duration: track.Duration,
		TC:       track.ID,
		Cover:    track.Cover,
		Platform: "youtube",
	}

	return trackInfo, nil
}

// downloadTrack handles the download of a track from YouTube.
// It returns the file path of the downloaded track or an error if the download fails.
func (y *YouTubeData) downloadTrack(ctx context.Context, info cache.TrackInfo, video bool) (string, error) {
	// Video mode: try API /video first, then fall back to yt-dlp.
	if video {
		if y.ApiUrl != "" && y.APIKey != "" {
			if filePath, err := y.downloadWithApiVideo(ctx, info.TC); err == nil {
				return filePath, nil
			}
		}
		return y.downloadWithYtDlp(ctx, info.TC, true)
	}

	// Audio mode: try API /song first, then fall back to yt-dlp.
	if y.ApiUrl != "" && y.APIKey != "" {
		if filePath, err := y.downloadWithApi(ctx, info.TC, false); err == nil {
			return filePath, nil
		}
	}

	// Fallback: pure yt-dlp audio.
	filePath, err := y.downloadWithYtDlp(ctx, info.TC, false)
	return filePath, err
}

// BuildYtdlpParams constructs the command-line parameters for yt-dlp to download media.
// It takes a video ID and a boolean indicating whether to download video or audio, and returns the corresponding parameters.
func (y *YouTubeData) BuildYtdlpParams(videoID string, video bool) []string {
	outputTemplate := filepath.Join(config.Conf.DownloadsDir, "%(id)s.%(ext)s")

	params := []string{
		"yt-dlp",
		"--no-warnings",
		"--quiet",
		"--geo-bypass",
		"--retries", "2",
		"--continue",
		"--no-part",
		"--concurrent-fragments", "3",
		"--socket-timeout", "10",
		"--throttled-rate", "100K",
		"--retry-sleep", "1",
		"--no-write-thumbnail",
		"--no-write-info-json",
		"--no-embed-metadata",
		"--no-embed-chapters",
		"--no-embed-subs",
		"--extractor-args", "youtube:player_js_version=actual",
		"-o", outputTemplate,
	}

	formatSelector := "bestaudio[ext=m4a]/bestaudio[ext=mp4]/bestaudio[ext=webm]/bestaudio/best"
	if video {
		formatSelector = "bestvideo[ext=mp4][height<=1080]+bestaudio[ext=m4a]/best[ext=mp4][height<=1080]"
		params = append(params, "--merge-output-format", "mp4")
	}
	params = append(params, "-f", formatSelector)

	if cookieFile := y.getCookieFile(); cookieFile != "" {
		params = append(params, "--cookies", cookieFile)
	} else if config.Conf.Proxy != "" {
		params = append(params, "--proxy", config.Conf.Proxy)
	}

	videoURL := "https://www.youtube.com/watch?v=" + videoID
	params = append(params, videoURL, "--print", "after_move:filepath")

	return params
}

// downloadWithYtDlp downloads media from YouTube using the yt-dlp command-line tool.
// It returns the file path of the downloaded track or an error if the download fails.
func (y *YouTubeData) downloadWithYtDlp(ctx context.Context, videoID string, video bool) (string, error) {
	ytdlpParams := y.BuildYtdlpParams(videoID, video)
	cmd := exec.CommandContext(ctx, ytdlpParams[0], ytdlpParams[1:]...)

	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := string(exitErr.Stderr)
			return "", fmt.Errorf("yt-dlp failed with exit code %d: %s", exitErr.ExitCode(), stderr)
		}

		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("yt-dlp timed out for video ID: %s", videoID)
		}

		return "", fmt.Errorf("an unexpected error occurred while downloading %s: %w", videoID, err)
	}

	downloadedPathStr := strings.TrimSpace(string(output))
	if downloadedPathStr == "" {
		return "", fmt.Errorf("no output path was returned for %s", videoID)
	}

	if _, err := os.Stat(downloadedPathStr); os.IsNotExist(err) {
		return "", fmt.Errorf("the file was not found at the reported path: %s", downloadedPathStr)
	}

	return downloadedPathStr, nil
}

// getCookieFile retrieves the path to a cookie file from the configured list.
// It returns the path to a randomly selected cookie file.
func (y *YouTubeData) getCookieFile() string {
	cookiesPath := config.Conf.CookiesPath
	if len(cookiesPath) == 0 {
		return ""
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(cookiesPath))))
	if err != nil {
		log.Printf("Could not generate a random number: %v", err)
		return cookiesPath[0]
	}

	return cookiesPath[n.Int64()]
}

// downloadWithApi downloads audio using your external Python API, mirroring
// the logic of download_song() in the Python code:
//   - {API_URL}/song/{video_id}?api={API_KEY}
//   - poll until status == "done"
//   - then download resp.Link into DownloadsDir/{video_id}.{format}
func (y *YouTubeData) downloadWithApi(ctx context.Context, videoID string, _ bool) (string, error) {
	downloadsDir := config.Conf.DownloadsDir

	// 1) Check local cache (downloads/{video_id}.mp3/m4a/webm)
	for _, ext := range []string{"mp3", "m4a", "webm"} {
		p := filepath.Join(downloadsDir, fmt.Sprintf("%s.%s", videoID, ext))
		if _, err := os.Stat(p); err == nil {
			// File already exists
			return p, nil
		}
	}

	if y.ApiUrl == "" || y.APIKey == "" {
		return "", fmt.Errorf("API URL or API key is not configured")
	}

	// 2) Build API URL: {API_URL}/song/{video_id}?api={API_KEY}
	songURL := fmt.Sprintf("%s/song/%s?api=%s", y.ApiUrl, videoID, y.APIKey)

	client := &http.Client{}
	var respData songAPIResponse

	// 3) Poll the API up to 10 times (status: downloading/done/other)
	for attempt := 0; attempt < 10; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, songURL, nil)
		if err != nil {
			return "", fmt.Errorf("failed to create API request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("API request failed: %w", err)
		}

		func() {
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("API request failed with status code %d", resp.StatusCode)
				return
			}

			if e := json.NewDecoder(resp.Body).Decode(&respData); e != nil {
				err = fmt.Errorf("failed to decode API response: %w", e)
				return
			}
		}()

		if err != nil {
			return "", err
		}

		status := strings.ToLower(strings.TrimSpace(respData.Status))

		switch status {
		case "done":
			if respData.Link == "" {
				return "", fmt.Errorf("API response did not provide a download URL")
			}
			goto DOWNLOAD

		case "downloading":
			// Wait 4 seconds like your Python download_song()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(4 * time.Second):
				// retry
			}

		default:
			msg := respData.Error
			if msg == "" {
				msg = respData.Message
			}
			if msg == "" {
				msg = fmt.Sprintf("unexpected status %q", status)
			}
			return "", fmt.Errorf("API error: %s", msg)
		}
	}

	return "", fmt.Errorf("max retries reached while waiting for API to finish processing")

DOWNLOAD:
	// 4) Download the file from respData.Link to downloads/{video_id}.{ext}
	format := strings.ToLower(strings.TrimSpace(respData.Format))
	if format == "" {
		format = "mp3"
	}

	if err := os.MkdirAll(downloadsDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create downloads dir: %w", err)
	}

	fileName := fmt.Sprintf("%s.%s", videoID, format)
	filePath := filepath.Join(downloadsDir, fileName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, respData.Link, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create download request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	out, err := os.Create(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return filePath, nil
}

// downloadWithApiVideo downloads video using your external Python API, mirroring
// the logic of download_video() in the Python code:
//   - {API_URL}/video/{video_id}?api={API_KEY}
//   - poll until status == "done"
//   - then download resp.Link into DownloadsDir/{video_id}.{format}
func (y *YouTubeData) downloadWithApiVideo(ctx context.Context, videoID string) (string, error) {
	downloadsDir := config.Conf.DownloadsDir

	// 1) Check local cache (downloads/{video_id}.mp4/webm/mkv)
	for _, ext := range []string{"mp4", "webm", "mkv"} {
		p := filepath.Join(downloadsDir, fmt.Sprintf("%s.%s", videoID, ext))
		if _, err := os.Stat(p); err == nil {
			// File already exists
			return p, nil
		}
	}

	if y.ApiUrl == "" || y.APIKey == "" {
		return "", fmt.Errorf("API URL or API key is not configured")
	}

	// 2) Build API URL: {API_URL}/video/{video_id}?api={API_KEY}
	videoURL := fmt.Sprintf("%s/video/%s?api=%s", y.ApiUrl, videoID, y.APIKey)

	client := &http.Client{}
	var respData songAPIResponse

	// 3) Poll the API up to 10 times (status: downloading/done/other)
	for attempt := 0; attempt < 10; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, nil)
		if err != nil {
			return "", fmt.Errorf("failed to create API request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("API request failed: %w", err)
		}

		func() {
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("API request failed with status code %d", resp.StatusCode)
				return
			}

			if e := json.NewDecoder(resp.Body).Decode(&respData); e != nil {
				err = fmt.Errorf("failed to decode API response: %w", e)
				return
			}
		}()

		if err != nil {
			return "", err
		}

		status := strings.ToLower(strings.TrimSpace(respData.Status))

		switch status {
		case "done":
			if respData.Link == "" {
				return "", fmt.Errorf("API response did not provide a download URL")
			}
			goto DOWNLOAD_VIDEO

		case "downloading":
			// Wait 8 seconds like your Python download_video()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(8 * time.Second):
				// retry
			}

		default:
			msg := respData.Error
			if msg == "" {
				msg = respData.Message
			}
			if msg == "" {
				msg = fmt.Sprintf("unexpected status %q", status)
			}
			return "", fmt.Errorf("API error: %s", msg)
		}
	}

	return "", fmt.Errorf("max retries reached while waiting for API to finish processing video")

DOWNLOAD_VIDEO:
	// 4) Download the file from respData.Link to downloads/{video_id}.{ext}
	format := strings.ToLower(strings.TrimSpace(respData.Format))
	if format == "" {
		format = "mp4"
	}

	if err := os.MkdirAll(downloadsDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create downloads dir: %w", err)
	}

	fileName := fmt.Sprintf("%s.%s", videoID, format)
	filePath := filepath.Join(downloadsDir, fileName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, respData.Link, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create download request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	out, err := os.Create(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return filePath, nil
}
