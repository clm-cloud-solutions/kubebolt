package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

type fixedBursts []insights.OperationalEpisode

func (f fixedBursts) ClusterAndStore(context.Context, string, time.Time, time.Time) ([]insights.OperationalEpisode, error) {
	return f, nil
}

// episodesByID is an EpisodeReader holding episodes by id; any other id is
// gone (pruned).
type episodesByID map[string]insights.Episode

func (e episodesByID) Window(context.Context, string, insights.EpisodeQuery) ([]insights.Episode, error) {
	return nil, nil
}
func (e episodesByID) Episode(_ context.Context, _ string, id string) (insights.Episode, []insights.Transition, error) {
	if ep, ok := e[id]; ok {
		return ep, nil, nil
	}
	return insights.Episode{}, nil, errors.New("not found")
}
func (episodesByID) ByFingerprint(context.Context, string, string, int32) ([]insights.Episode, error) {
	return nil, nil
}
func (episodesByID) IgnoredRate(context.Context, string, time.Duration) (map[string][2]int64, error) {
	return nil, nil
}

func burstRouter(h *handlers) http.Handler {
	r := chi.NewRouter()
	r.Get("/insights/operational-episodes", h.handleOperationalEpisodes)
	r.Get("/insights/operational-episodes/{id}/episodes", h.handleBurstEpisodes)
	return r
}

func getBurst(t *testing.T, h *handlers, path string, p *auth.APIPrincipal) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if p != nil {
		req = req.WithContext(auth.WithAPIPrincipal(req.Context(), p))
	}
	rec := httptest.NewRecorder()
	burstRouter(h).ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func episodeIDs(body map[string]any) []string {
	var ids []string
	for _, e := range body["episodes"].([]any) {
		ids = append(ids, e.(map[string]any)["id"].(string))
	}
	return ids
}

func burstFixture() *handlers {
	return &handlers{
		operational: fixedBursts{{ID: "b1", Clusters: []string{"uid-a", "uid-b"}, MemberIDs: []string{"e1", "e2", "e4", "pruned"}}},
		episodes: episodesByID{
			"e1": {ID: "e1", ClusterID: "uid-a"},
			"e2": {ID: "e2", ClusterID: "uid-a"},
			"e3": {ID: "e3", ClusterID: "uid-a"}, // in the window, not a member
			"e4": {ID: "e4", ClusterID: "uid-b"},
		},
	}
}

// "See the N episodes" lands on the burst's members — not on whatever else
// overlapped its window — and a member the history no longer holds is counted,
// so the page can explain a shorter list (finding #75).
func TestBurstEpisodes_ExactlyTheMembers(t *testing.T) {
	code, body := getBurst(t, burstFixture(), "/insights/operational-episodes/b1/episodes", nil)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	if got := episodeIDs(body); len(got) != 3 || got[0] != "e1" || got[1] != "e2" || got[2] != "e4" {
		t.Errorf("episodes = %v, want the members e1 e2 e4 and no e3", got)
	}
	if body["missing"] != float64(1) {
		t.Errorf("missing = %v, want the one pruned member", body["missing"])
	}
}

func TestBurstEpisodes_LeavesOutClustersTheCallerMayNotRead(t *testing.T) {
	code, body := getBurst(t, burstFixture(), "/insights/operational-episodes/b1/episodes", narrowedKey("uid-a"))
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	if got := episodeIDs(body); len(got) != 2 || got[0] != "e1" || got[1] != "e2" {
		t.Errorf("episodes = %v, want only uid-a's members", got)
	}
}

func TestBurstEpisodes_UnknownBurstIs404(t *testing.T) {
	if code, _ := getBurst(t, burstFixture(), "/insights/operational-episodes/nope/episodes", nil); code != http.StatusNotFound {
		t.Errorf("status %d, want 404 for a burst that no longer forms in the window", code)
	}
}

// One GET recomputes and rewrites every burst in its window: both endpoints
// refuse an inverted window and one wider than 31 days.
func TestBurstWindow_IsBounded(t *testing.T) {
	now := time.Now().UTC()
	q := func(since, until time.Time) string {
		return "?" + url.Values{"since": {since.Format(time.RFC3339)}, "until": {until.Format(time.RFC3339)}}.Encode()
	}
	for _, path := range []string{"/insights/operational-episodes", "/insights/operational-episodes/b1/episodes"} {
		if code, _ := getBurst(t, burstFixture(), path+q(now.Add(-40*24*time.Hour), now), nil); code != http.StatusBadRequest {
			t.Errorf("%s: 40-day window got %d, want 400", path, code)
		}
		if code, _ := getBurst(t, burstFixture(), path+q(now, now.Add(-time.Hour)), nil); code != http.StatusBadRequest {
			t.Errorf("%s: inverted window got %d, want 400", path, code)
		}
	}
	if code, body := getBurst(t, burstFixture(), "/insights/operational-episodes/b1/episodes"+q(now.Add(-30*24*time.Hour), now), nil); code != http.StatusOK {
		t.Errorf("the UI's 30-day preset got %d (%v), want 200", code, body)
	}
}
