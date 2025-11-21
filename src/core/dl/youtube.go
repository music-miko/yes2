package dl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"ashokshau/tgmusic/src/core/cache"
	"ashokshau/tgmusic/src/config"
)

type songAPIResponse struct {
	Status  string `json:"status"`
	Link    string `json:"link"`
	Format  string `json:"format"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

type YouTubeData struct {
	Query    string
	ApiUrl   string
	APIKey   string
	Patterns map[string]*regexp.Regexp
}

var youtubePatterns = map[string]*regexp.Regexp{
	"youtube":   regexp.MustCompile(`^(?:https?://)?(?:www\.)?youtube\.com/watch\?v=([\w-]{11})`),
	"youtu_be":  regexp.MustCompile(`^(?:https?://)?(?:www\.)?youtu\.be/([\w-]{11})`),
	"shorts":    regexp.MustCompile(`^(?:https?://)?(?:www\.)?youtube\.com/shorts/([\w-]{11})`),
}

func NewYouTubeData(query string) *YouTubeData {
	return &YouTubeData{
		Query:    strings.TrimSpace(query),
		ApiUrl:   strings.TrimRight(config.Conf.ApiUrl, "/"),
		APIKey:   config.Conf.ApiKey,
		Patterns: youtubePatterns,
	}
}

func (y *YouTubeData) normalizeYouTubeURL(url string) string {
	url = strings.TrimSpace(url)
	if strings.Contains(url, "youtu.be/") {
		id := strings.Split(strings.Split(url, "youtu.be/")[1], "?")[0]
		return "https://www.youtube.com/watch?v=" + id
	}
	if strings.Contains(url, "youtube.com/shorts/") {
		id := strings.Split(strings.Split(url, "youtube.com/shorts/")[1], "?")[0]
		return "https://www.youtube.com/watch?v=" + id
	}
	return url
}

func (y *YouTubeData) extractVideoID(url string) string {
	url = y.normalizeYouTubeURL(url)
	for _, p := range y.Patterns {
		if m := p.FindStringSubmatch(url); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func (y *YouTubeData) IsValid() bool {
	for _, p := range y.Patterns {
		if p.MatchString(y.Query) {
			return true
		}
	}
	return false
}

// ----------- SEARCH -------------

func (y *YouTubeData) Search(ctx context.Context) (cache.PlatformTracks, error) {
	tracks, err := searchYouTube(y.Query)
	if err != nil {
		return cache.PlatformTracks{}, err
	}
	return cache.PlatformTracks{Results: tracks}, nil
}

// ----------- GET INFO -------------

func (y *YouTubeData) GetInfo(ctx context.Context) (cache.PlatformTracks, error) {
	if !y.IsValid() {
		return cache.PlatformTracks{}, errors.New("invalid YouTube URL")
	}

	videoID := y.extractVideoID(y.Query)
	if videoID == "" {
		return cache.PlatformTracks{}, errors.New("cannot extract video id")
	}

	tracks, err := searchYouTube(y.Query)
	if err != nil {
		return cache.PlatformTracks{}, err
	}

	for _, t := range tracks {
		if t.ID == videoID {
			return cache.PlatformTracks{Results: []cache.MusicTrack{t}}, nil
		}
	}

	return cache.PlatformTracks{}, errors.New("video not found")
}

// ----------- TRACK INFO -------------

func (y *YouTubeData) GetTrack(ctx context.Context) (cache.TrackInfo, error) {
	info, err := y.GetInfo(ctx)
	if err != nil {
		return cache.TrackInfo{}, err
	}
	t := info.Results[0]

	return cache.TrackInfo{
		URL:      t.URL,
		CdnURL:   "None",
		Key:      "None",
		Name:     t.Name,
		Duration: t.Duration,
		TC:       t.ID,
		Cover:    t.Cover,
		Platform: "youtube",
	}, nil
}

// ----------- DOWNLOAD -----------

func (y *YouTubeData) downloadTrack(ctx context.Context, info cache.TrackInfo, video bool) (string, error) {
	if video {
		if y.APIKey != "" {
			if p, err := y.downloadWithApiVideo(ctx, info.TC); err == nil {
				return p, nil
			}
		}
		return y.downloadWithYtDlp(ctx, info.TC, true)
	}

	if y.ApiUrl != "" && y.APIKey != "" {
		if p, err := y.downloadWithApi(ctx, info.TC); err == nil {
			return p, nil
		}
	}

	return y.downloadWithYtDlp(ctx, info.TC, false)
}

// ----------- YT-DLP DOWNLOAD -------------

func (y *YouTubeData) BuildYtdlpParams(videoID string, video bool) []string {
	out := filepath.Join(config.Conf.DownloadsDir, "%(id)s.%(ext)s")

	p := []string{
		"yt-dlp",
		"--no-warnings",
		"--quiet",
		"--geo-bypass",
		"-o", out,
	}

	format := "bestaudio/best"
	if video {
		format = "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best"
	}

	p = append(p, "-f", format)
	p = append(p, "https://www.youtube.com/watch?v="+videoID, "--print", "after_move:filepath")

	return p
}

func (y *YouTubeData) downloadWithYtDlp(ctx context.Context, videoID string, video bool) (string, error) {
	args := y.BuildYtdlpParams(videoID, video)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("yt-dlp failed: %s | %s", err, string(out))
	}

	path := strings.TrimSpace(string(out))
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("file not created: %s", path)
	}
	return path, nil
}

// ----------- API DOWNLOAD (AUDIO) -----------

func (y *YouTubeData) downloadWithApi(ctx context.Context, videoID string) (string, error) {
	url := fmt.Sprintf("%s/song/%s?api=%s", y.ApiUrl, videoID, y.APIKey)

	client := &http.Client{}
	var respJson songAPIResponse

	for i := 0; i < 10; i++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		json.NewDecoder(resp.Body).Decode(&respJson)
		resp.Body.Close()

		if respJson.Status == "done" && respJson.Link != "" {
			break
		}
		time.Sleep(4 * time.Second)
	}

	if respJson.Link == "" {
		return "", errors.New("API audio failed")
	}

	return y.downloadFromURL(videoID, respJson.Format, respJson.Link)
}

// ----------- API DOWNLOAD (VIDEO) -----------

func (y *YouTubeData) downloadWithApiVideo(ctx context.Context, videoID string) (string, error) {
	url := fmt.Sprintf("https://api.video.thequickearn.xyz/video/%s?api=%s", videoID, y.APIKey)

	client := &http.Client{}
	var respJson songAPIResponse

	for i := 0; i < 10; i++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		json.NewDecoder(resp.Body).Decode(&respJson)
		resp.Body.Close()

		if respJson.Status == "done" && respJson.Link != "" {
			break
		}
		time.Sleep(8 * time.Second)
	}

	if respJson.Link == "" {
		return "", errors.New("API video failed")
	}

	return y.downloadFromURL(videoID, respJson.Format, respJson.Link)
}

// ----------- ACTUAL HTTP DOWNLOAD -----------

func (y *YouTubeData) downloadFromURL(videoID, format, dlURL string) (string, error) {
	if format == "" {
		format = "mp3"
	}

	os.MkdirAll(config.Conf.DownloadsDir, 0755)
	filename := filepath.Join(config.Conf.DownloadsDir, fmt.Sprintf("%s.%s", videoID, format))

	resp, err := http.Get(dlURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	f, err := os.Create(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()

	io.Copy(f, resp.Body)
	return filename, nil
}
