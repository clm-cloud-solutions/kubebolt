package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

func requestAs(p *auth.APIPrincipal) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if p != nil {
		r = r.WithContext(auth.WithAPIPrincipal(r.Context(), p))
	}
	return r
}

func narrowedKey(ids ...string) *auth.APIPrincipal {
	return &auth.APIPrincipal{Type: auth.TokenTypeAPIKey, Role: auth.RoleViewer, Clusters: ids}
}

// Only an API key WITH a list is narrowed: a session, a service token and a
// key without a list keep whatever the org and team rules decided.
func TestTokenClusterSet_OnlyNarrowedKeys(t *testing.T) {
	for name, p := range map[string]*auth.APIPrincipal{
		"session":       nil,
		"service token": {Type: auth.TokenTypeService, Clusters: []string{"uid-a"}},
		"key, no list":  {Type: auth.TokenTypeAPIKey},
	} {
		if _, narrowed := tokenClusterSet(requestAs(p)); narrowed {
			t.Errorf("%s: narrowed, want not", name)
		}
	}
	if _, narrowed := tokenClusterSet(requestAs(narrowedKey("uid-a"))); !narrowed {
		t.Error("key with a list: not narrowed")
	}
}

// "No narrowing" (single-tenant, an admin-role key) becomes the list itself;
// an already narrowed answer is intersected, never widened.
func TestNarrowIDsByToken(t *testing.T) {
	r := requestAs(narrowedKey("uid-a", "uid-b"))

	ids, narrowed := narrowIDsByToken(r, nil, false)
	sort.Strings(ids)
	if !narrowed || !reflect.DeepEqual(ids, []string{"uid-a", "uid-b"}) {
		t.Fatalf("from no narrowing: %v %v", ids, narrowed)
	}
	ids, narrowed = narrowIDsByToken(r, []string{"uid-b", "uid-c"}, true)
	if !narrowed || !reflect.DeepEqual(ids, []string{"uid-b"}) {
		t.Fatalf("intersection: %v %v", ids, narrowed)
	}
	ids, narrowed = narrowIDsByToken(requestAs(nil), []string{"uid-c"}, true)
	if !narrowed || !reflect.DeepEqual(ids, []string{"uid-c"}) {
		t.Fatalf("a session is left as it was: %v %v", ids, narrowed)
	}
}

func TestFilterClustersByToken(t *testing.T) {
	h := &handlers{}
	all := []cluster.ClusterInfo{{Context: "agent:uid-a", ClusterID: "uid-a"}, {Context: "agent:uid-b", ClusterID: "uid-b"}}
	got := h.filterClustersByToken(requestAs(narrowedKey("uid-b")), all)
	if len(got) != 1 || got[0].ClusterID != "uid-b" {
		t.Fatalf("got %v", got)
	}
	if got := h.filterClustersByToken(requestAs(nil), all); len(got) != 2 {
		t.Fatalf("a session lost clusters: %v", got)
	}
}

// A burst that reached a readable cluster is shown, naming only that cluster;
// one that did not is dropped.
func TestFilterBursts(t *testing.T) {
	may := func(id string) bool { return id == "uid-a" }
	ops := []insights.OperationalEpisode{
		{ID: "both", Clusters: []string{"uid-a", "uid-b"}},
		{ID: "other", Clusters: []string{"uid-b"}},
	}
	got := filterBursts(ops, may)
	if len(got) != 1 || got[0].ID != "both" || !reflect.DeepEqual(got[0].Clusters, []string{"uid-a"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestFilterEpisodes(t *testing.T) {
	eps := []insights.Episode{{ID: "1", ClusterID: "uid-a"}, {ID: "2", ClusterID: "uid-b"}}
	got := filterEpisodes(eps, func(id string) bool { return id == "uid-b" })
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("got %+v", got)
	}
}

type pagingEpisodes struct{ rows []insights.Episode }

// Window pages newest first like the store, honouring ClusterID and Limit.
func (p pagingEpisodes) Window(_ context.Context, _ string, q insights.EpisodeQuery) ([]insights.Episode, error) {
	var out []insights.Episode
	for _, e := range p.rows {
		if q.ClusterID == "" || e.ClusterID == q.ClusterID {
			out = append(out, e)
		}
	}
	if int(q.Limit) < len(out) {
		out = out[:q.Limit]
	}
	return out, nil
}
func (pagingEpisodes) Episode(context.Context, string, string) (insights.Episode, []insights.Transition, error) {
	return insights.Episode{}, nil, nil
}
func (pagingEpisodes) ByFingerprint(context.Context, string, string, int32) ([]insights.Episode, error) {
	return nil, nil
}
func (pagingEpisodes) IgnoredRate(context.Context, string, time.Duration) (map[string][2]int64, error) {
	return nil, nil
}

// Found in vivo: the org's newest page was all another cluster's episodes, so
// filtering it left a narrowed caller with none while its cluster had 25.
func TestWindowForClusters_ReadsTheCallersClustersNotAFilteredOrgPage(t *testing.T) {
	now := time.Now()
	var rows []insights.Episode
	for i := 0; i < 10; i++ { // the other cluster is newer and fills a page
		rows = append(rows, insights.Episode{ID: "b", ClusterID: "uid-b", LastSeen: now.Add(-time.Duration(i) * time.Minute)})
	}
	rows = append(rows, insights.Episode{ID: "a1", ClusterID: "uid-a", LastSeen: now.Add(-time.Hour)},
		insights.Episode{ID: "a2", ClusterID: "uid-a", LastSeen: now.Add(-2 * time.Hour)})
	got, err := windowForClusters(context.Background(), pagingEpisodes{rows}, "org", insights.EpisodeQuery{Limit: 5}, []string{"uid-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a1" || got[1].ID != "a2" {
		t.Fatalf("got %+v, want the caller's two episodes newest first", got)
	}
}
