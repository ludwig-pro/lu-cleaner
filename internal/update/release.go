package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	modulePath    = "github.com/ludwig-pro/lu-cleaner"
	latestURL     = "https://api.github.com/repos/ludwig-pro/lu-cleaner/releases/latest"
	archiveName   = "lu-cleaner_macOS_universal.tar.gz"
	checksumsName = "checksums.txt"
	maxMetadata   = 1 << 20
)

type version struct{ major, minor, patch uint64 }

var stablePattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func parseStableVersion(s string) (version, error) {
	m := stablePattern.FindStringSubmatch(s)
	if m == nil {
		return version{}, fmt.Errorf("unsupported version")
	}
	var nums [3]uint64
	for i := range nums {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return version{}, err
		}
		nums[i] = n
	}
	return version{nums[0], nums[1], nums[2]}, nil
}

func (v version) String() string { return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch) }
func (v version) newer(o version) bool {
	if v.major != o.major {
		return v.major > o.major
	}
	if v.minor != o.minor {
		return v.minor > o.minor
	}
	return v.patch > o.patch
}

func installedVersion(value string, info *debug.BuildInfo) (version, bool) {
	v, err := parseStableVersion(value)
	if err == nil {
		return v, true
	}
	if value != "dev" || info == nil || info.Main.Path != modulePath || info.Main.Replace != nil {
		return version{}, false
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.modified" && setting.Value == "true" {
			return version{}, false
		}
	}
	v, err = parseStableVersion(info.Main.Version)
	return v, err == nil
}

type release struct {
	version             version
	archiveURL, sumsURL string
}

func releaseAssetURL(tag, name string) string {
	return "https://github.com/ludwig-pro/lu-cleaner/releases/download/" + tag + "/" + name
}

func latestStable(ctx context.Context, client *http.Client) (release, error) {
	data, err := fetchBounded(ctx, client, latestURL, maxMetadata)
	if err != nil {
		return release{}, err
	}
	var body struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return release{}, err
	}
	v, err := parseStableVersion(body.Tag)
	if err != nil || body.Draft || body.Prerelease {
		return release{}, fmt.Errorf("unsupported release")
	}
	r := release{version: v}
	for _, asset := range body.Assets {
		switch asset.Name {
		case archiveName:
			if r.archiveURL != "" || asset.URL != releaseAssetURL(body.Tag, archiveName) {
				return release{}, fmt.Errorf("unexpected archive asset")
			}
			r.archiveURL = asset.URL
		case checksumsName:
			if r.sumsURL != "" || asset.URL != releaseAssetURL(body.Tag, checksumsName) {
				return release{}, fmt.Errorf("unexpected checksum asset")
			}
			r.sumsURL = asset.URL
		}
	}
	if r.archiveURL == "" || r.sumsURL == "" {
		return release{}, fmt.Errorf("missing release assets")
	}
	return r, nil
}

func newHTTPClient() *http.Client {
	return &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 || !allowedURL(req.URL) {
			return fmt.Errorf("unsupported release redirect")
		}
		return nil
	}}
}

func allowedURL(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" {
		return false
	}
	switch u.Host {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com":
		return true
	}
	return false
}

func response(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	u, err := url.Parse(address)
	if err != nil || !allowedURL(u) {
		return nil, fmt.Errorf("unsupported release URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lu-cleaner-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("release HTTP status %d", resp.StatusCode)
	}
	return resp, nil
}

func fetchBounded(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	resp, err := response(ctx, client, address)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("release response too large")
	}
	return data, nil
}

func checksum(data []byte) ([32]byte, error) {
	var result [32]byte
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if len(line) < 67 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return result, fmt.Errorf("invalid checksum entry")
		}
		var digest [32]byte
		for i := range digest {
			n, err := strconv.ParseUint(line[i*2:i*2+2], 16, 8)
			if err != nil {
				return result, fmt.Errorf("invalid checksum digest")
			}
			digest[i] = byte(n)
		}
		if line[66:] == archiveName {
			if found {
				return result, fmt.Errorf("duplicate archive checksum")
			}
			found = true
			result = digest
		}
	}
	if !found {
		return result, fmt.Errorf("missing archive checksum")
	}
	return result, nil
}
