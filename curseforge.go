package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	curseforgeAPI        = "https://api.curseforge.com/v1"
	minecraftGameID      = 432
	minecraftModsClassID = 6
	forgeLoaderID        = 1
)

var jarNamePattern = regexp.MustCompile(`(?i)\.jar$`)

func parseProjectURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" || (u.Hostname() != "www.curseforge.com" && u.Hostname() != "curseforge.com") || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("expected https://www.curseforge.com/minecraft/mc-mods/<slug> without query or fragment")
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	const prefix = "/minecraft/mc-mods/"
	if !strings.HasPrefix(path, prefix) {
		return "", errors.New("not a CurseForge Minecraft mod project URL")
	}
	slug := strings.TrimPrefix(path, prefix)
	if !slugPattern.MatchString(slug) {
		return "", errors.New("invalid project slug (only letters, numbers and '-')")
	}
	return slug, nil
}

func projectURL(slug string) string {
	return "https://www.curseforge.com/minecraft/mc-mods/" + slug
}

type Mod struct {
	ID                   int64  `json:"id"`
	GameID               int    `json:"gameId"`
	ClassID              int    `json:"classId"`
	Name                 string `json:"name"`
	Slug                 string `json:"slug"`
	IsAvailable          bool   `json:"isAvailable"`
	AllowModDistribution bool   `json:"allowModDistribution"`
}

type Hash struct {
	Value string `json:"value"`
	Algo  int    `json:"algo"` // CurseForge: 1=SHA1, 2=MD5
}

type Dependency struct {
	ModID        int64 `json:"modId"`
	RelationType int   `json:"relationType"` // 3=required
}

type File struct {
	ID                   int64        `json:"id"`
	ModID                int64        `json:"modId"`
	FileName             string       `json:"fileName"`
	FileDate             string       `json:"fileDate"`
	FileLength           int64        `json:"fileLength"`
	DownloadURL          string       `json:"downloadUrl"`
	GameVersions         []string     `json:"gameVersions"`
	Hashes               []Hash       `json:"hashes"`
	Dependencies         []Dependency `json:"dependencies"`
	ReleaseType          int          `json:"releaseType"` // 1=release 2=beta 3=alpha
	IsAvailable          bool         `json:"isAvailable"`
	IsServerPack         bool         `json:"isServerPack"`
	ParentProjectFileID  int64        `json:"parentProjectFileId"`
	IsEarlyAccessContent bool         `json:"isEarlyAccessContent"`
}

type Pagination struct {
	Index       int `json:"index"`
	PageSize    int `json:"pageSize"`
	ResultCount int `json:"resultCount"`
	TotalCount  int `json:"totalCount"`
}

type Client struct {
	APIBase string
	Key     string
	HTTP    *http.Client
}

func newClient(key string) *Client {
	return &Client{
		APIBase: curseforgeAPI,
		Key:     key,
		HTTP: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 || req.URL.Scheme != "https" || req.URL.Hostname() != "api.curseforge.com" {
				return errors.New("unexpected API redirect")
			}
			req.Header.Set("x-api-key", key)
			return nil
		}},
	}
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, target any) error {
	endpoint := strings.TrimRight(c.APIBase, "/") + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("x-api-key", c.Key)
		req.Header.Set("Accept", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt < 2 {
				if err := retryPause(ctx, attempt, ""); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("GET %s: %w", path, err)
		}
		if resp.StatusCode == http.StatusOK {
			dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
			err := dec.Decode(target)
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("invalid API JSON (%s): %w", path, err)
			}
			return nil
		}
		// Never print the API key or the full CDN URL in errors.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("CurseForge API rejected credentials (%d); check CURSEFORGE_API_KEY / distribution permissions", resp.StatusCode)
		}
		if (resp.StatusCode == 429 || resp.StatusCode >= 500) && attempt < 2 {
			if err := retryPause(ctx, attempt, resp.Header.Get("Retry-After")); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("CurseForge API %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	return errors.New("exhausted retries")
}

func retryPause(ctx context.Context, attempt int, after string) error {
	delay := time.Duration(1<<attempt) * time.Second
	if n, err := strconv.Atoi(after); err == nil && n > 0 {
		delay = time.Duration(n) * time.Second
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) searchMod(ctx context.Context, slug string) (Mod, error) {
	query := url.Values{
		"gameId":   {strconv.Itoa(minecraftGameID)},
		"classId":  {strconv.Itoa(minecraftModsClassID)},
		"slug":     {slug},
		"pageSize": {"50"},
	}
	var resp struct {
		Data []Mod `json:"data"`
	}
	if err := c.getJSON(ctx, "/mods/search", query, &resp); err != nil {
		return Mod{}, err
	}
	for _, m := range resp.Data {
		if m.Slug == slug && m.GameID == minecraftGameID && m.ClassID == minecraftModsClassID {
			if !m.IsAvailable || !m.AllowModDistribution {
				return Mod{}, fmt.Errorf("%s: project unavailable for third-party API distribution", slug)
			}
			return m, nil
		}
	}
	return Mod{}, fmt.Errorf("project not found by exact slug: %s", slug)
}

func (c *Client) getMod(ctx context.Context, modID int64) (Mod, error) {
	var resp struct {
		Data Mod `json:"data"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/mods/%d", modID), nil, &resp); err != nil {
		return Mod{}, err
	}
	m := resp.Data
	if m.ID != modID || m.GameID != minecraftGameID || m.ClassID != minecraftModsClassID || !m.IsAvailable || !m.AllowModDistribution {
		return Mod{}, fmt.Errorf("mod %d unavailable or not authorized for distribution", modID)
	}
	return m, nil
}

func (c *Client) getFiles(ctx context.Context, modID int64, version string) ([]File, error) {
	const pageSize = 50
	result := make([]File, 0, pageSize)
	for offset := 0; offset < 10000; offset += pageSize {
		query := url.Values{
			"gameVersion":   {version},
			"modLoaderType": {strconv.Itoa(forgeLoaderID)},
			"index":         {strconv.Itoa(offset)},
			"pageSize":      {strconv.Itoa(pageSize)},
		}
		var resp struct {
			Data       []File     `json:"data"`
			Pagination Pagination `json:"pagination"`
		}
		if err := c.getJSON(ctx, fmt.Sprintf("/mods/%d/files", modID), query, &resp); err != nil {
			return nil, err
		}
		result = append(result, resp.Data...)
		if len(resp.Data) == 0 || len(resp.Data) < pageSize || (resp.Pagination.TotalCount > 0 && offset+len(resp.Data) >= resp.Pagination.TotalCount) {
			return result, nil
		}
	}
	return nil, fmt.Errorf("mod %d: file list exceeds API pagination limit", modID)
}

func hasGameVersion(f File, version string) bool {
	for _, v := range f.GameVersions {
		if v == version {
			return true
		}
	}
	return false
}

// API filters Forge on the server. A second tag check rules out explicitly
// incompatible loaders when the file carries such tags.
func loaderCompatible(f File) bool {
	var forge, alien bool
	for _, v := range f.GameVersions {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "forge":
			forge = true
		case "fabric", "quilt", "neoforge", "neo forge":
			alien = true
		}
	}
	return forge || !alien
}

func releaseAllowed(release int, channel string) bool {
	switch channel {
	case "release":
		return release == 1
	case "beta":
		return release == 1 || release == 2
	case "all":
		return release >= 1 && release <= 3
	}
	return false
}

func safeJarName(name string) bool {
	if name == "" || len(name) > 240 || !jarNamePattern.MatchString(name) || name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return false
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
		(len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
		return false
	}
	return !strings.ContainsAny(name, "/\\:*?\"<>|\x00\r\n\t")
}

func selectLatest(files []File, version, channel string) (File, error) {
	candidates := make([]File, 0, len(files))
	for _, f := range files {
		if f.ID <= 0 || !f.IsAvailable || f.IsServerPack || f.ParentProjectFileID > 0 || f.IsEarlyAccessContent || !safeJarName(f.FileName) || !hasGameVersion(f, version) || !loaderCompatible(f) || !releaseAllowed(f.ReleaseType, channel) {
			continue
		}
		// FileDate should always exist on published files.
		if _, err := time.Parse(time.RFC3339, f.FileDate); err != nil {
			continue
		}
		candidates = append(candidates, f)
	}
	if len(candidates) == 0 {
		return File{}, fmt.Errorf("no compatible Forge %s JAR (channel=%s)", version, channel)
	}
	sort.Slice(candidates, func(i, j int) bool {
		ai, _ := time.Parse(time.RFC3339, candidates[i].FileDate)
		aj, _ := time.Parse(time.RFC3339, candidates[j].FileDate)
		if ai.Equal(aj) {
			return candidates[i].ID > candidates[j].ID
		}
		return ai.After(aj)
	})
	return candidates[0], nil
}

func (c *Client) getDownloadURL(ctx context.Context, modID, fileID int64) (string, error) {
	var resp struct {
		Data *string `json:"data"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/mods/%d/files/%d/download-url", modID, fileID), nil, &resp); err != nil {
		return "", err
	}
	if resp.Data == nil || *resp.Data == "" {
		return "", fmt.Errorf("mod %d file %d: direct distribution URL withheld by CurseForge", modID, fileID)
	}
	u, err := url.Parse(*resp.Data)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", errors.New("CurseForge returned invalid or insecure download URL")
	}
	return *resp.Data, nil
}
