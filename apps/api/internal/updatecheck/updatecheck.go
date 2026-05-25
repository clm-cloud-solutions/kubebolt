// Package updatecheck queries the KubeBolt GitHub releases API and
// reports whether a newer stable version is available than the one
// the binary was built with.
//
// The service is intentionally minimal:
//   - On-demand fetch with TTL cache (default 6h) — no background poll
//   - Filters out prereleases and drafts before picking the highest semver
//   - Silent on errors — caller receives last good cache or nil result
//   - Skipped entirely when the binary is a dev build (no version stamp)
//
// Air-gapped operators disable the whole thing via the
// KUBEBOLT_UPDATE_CHECK_ENABLED env var or the admin runtime config
// toggle (both honored at the HTTP handler layer, not here).
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver/v4"
)

const (
	// DefaultRepo is the GitHub repo checked for releases.
	DefaultRepo = "clm-cloud-solutions/kubebolt"

	// DefaultCacheTTL is how long a successful fetch is reused before
	// the next caller triggers a new GitHub round-trip.
	DefaultCacheTTL = 6 * time.Hour

	// minRetryAfterErr throttles re-fetch attempts when the previous
	// fetch failed — protects against hammering GitHub during outages
	// or rate-limit cool-downs.
	minRetryAfterErr = 5 * time.Minute

	githubAPIBase = "https://api.github.com"
)

// Release mirrors the subset of fields the GitHub releases API returns
// that this package cares about. Extra fields are ignored by json.Unmarshal.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	PublishedAt time.Time `json:"published_at"`
}

// Service is the on-demand update checker. Safe for concurrent use.
type Service struct {
	httpClient     *http.Client
	repo           string
	currentVersion string
	cacheTTL       time.Duration

	mu        sync.RWMutex
	latest    *Release
	fetchedAt time.Time
	lastErr   error
}

// New constructs a Service. A currentVersion of "dev" (the unset
// ldflag default) or an empty string disables all GitHub traffic —
// the service short-circuits to "no update available".
func New(currentVersion, repo string, cacheTTL time.Duration) *Service {
	if repo == "" {
		repo = DefaultRepo
	}
	if cacheTTL <= 0 {
		cacheTTL = DefaultCacheTTL
	}
	return &Service{
		httpClient:     &http.Client{Timeout: 10 * time.Second},
		repo:           repo,
		currentVersion: currentVersion,
		cacheTTL:       cacheTTL,
	}
}

// CurrentVersion returns the running version as the binary reports it.
func (s *Service) CurrentVersion() string { return s.currentVersion }

// IsDevBuild reports whether the running binary is a dev / unstamped
// build. Used to skip GitHub traffic for local development.
func (s *Service) IsDevBuild() bool {
	v := strings.TrimSpace(strings.TrimPrefix(s.currentVersion, "v"))
	return v == "" || v == "dev" || strings.HasPrefix(v, "dev")
}

// Latest returns the cached latest stable release, refreshing the cache
// when expired. Returns (nil, nil) on dev builds or when no successful
// fetch has happened yet. Errors are returned but do NOT replace the
// last known good cache.
func (s *Service) Latest(ctx context.Context) (*Release, error) {
	if s.IsDevBuild() {
		return nil, nil
	}

	s.mu.RLock()
	if s.latest != nil && time.Since(s.fetchedAt) < s.cacheTTL {
		rel := s.latest
		s.mu.RUnlock()
		return rel, nil
	}
	if s.lastErr != nil && time.Since(s.fetchedAt) < minRetryAfterErr {
		err := s.lastErr
		cached := s.latest
		s.mu.RUnlock()
		return cached, err
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest != nil && time.Since(s.fetchedAt) < s.cacheTTL {
		return s.latest, nil
	}
	if s.lastErr != nil && time.Since(s.fetchedAt) < minRetryAfterErr {
		return s.latest, s.lastErr
	}

	rel, err := s.fetchLatestStable(ctx)
	s.fetchedAt = time.Now()
	if err != nil {
		s.lastErr = err
		slog.Warn("update-check: GitHub fetch failed",
			slog.String("repo", s.repo),
			slog.String("err", err.Error()),
		)
		return s.latest, err
	}
	s.lastErr = nil
	s.latest = rel
	return rel, nil
}

// IsUpdateAvailable returns true when the given latest tag parses as a
// semver greater than the binary's current version. Dev builds always
// return false; unparseable inputs return false.
func (s *Service) IsUpdateAvailable(latestTag string) bool {
	if s.IsDevBuild() {
		return false
	}
	cur, err := parseSemver(s.currentVersion)
	if err != nil {
		return false
	}
	lat, err := parseSemver(latestTag)
	if err != nil {
		return false
	}
	return lat.GT(cur)
}

// parseSemver tolerates the "v" prefix and the git-describe trailing
// shape ("v1.12.0-2-gabc1234" or "v1.12.0-dirty") that ldflags pulls
// in when the working tree isn't on a clean tag.
func parseSemver(v string) (semver.Version, error) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if v == "" {
		return semver.Version{}, errors.New("empty version")
	}
	if i := strings.Index(v, "-"); i > 0 {
		v = v[:i]
	}
	return semver.Parse(v)
}

func (s *Service) fetchLatestStable(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=10", githubAPIBase, s.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "kubebolt-update-check")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api %d: %s", resp.StatusCode, truncate(body, 200))
	}

	var releases []Release
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	return pickHighestStable(releases)
}

// pickHighestStable returns the release with the highest semver,
// ignoring prereleases, drafts, and tags that don't parse as semver.
func pickHighestStable(releases []Release) (*Release, error) {
	var best *Release
	var bestVer semver.Version
	for i := range releases {
		r := &releases[i]
		if r.Prerelease || r.Draft {
			continue
		}
		v, err := parseSemver(r.TagName)
		if err != nil {
			continue
		}
		if best == nil || v.GT(bestVer) {
			best = r
			bestVer = v
		}
	}
	if best == nil {
		return nil, errors.New("no stable releases found")
	}
	return best, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
