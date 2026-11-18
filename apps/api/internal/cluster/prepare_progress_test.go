package cluster

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/agent/channel"
)

func protoList(t *testing.T, remaining *int64) []byte {
	t.Helper()
	list := &appsv1.ReplicaSetList{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSetList"},
		ListMeta: metav1.ListMeta{ResourceVersion: "123", Continue: "eyJ2IjoibWV0YS5rOHMuaW8vdjEifQ", RemainingItemCount: remaining},
	}
	for i := 0; i < 50; i++ {
		list.Items = append(list.Items, appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", Namespace: "ns", Labels: map[string]string{"pad": string(bytes.Repeat([]byte("x"), 200))}}})
	}
	var buf bytes.Buffer
	if err := protobuf.NewSerializer(scheme.Scheme, scheme.Scheme).Encode(list, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRemainingItemCount_ProtobufAndJSON(t *testing.T) {
	n := int64(5132)
	body := protoList(t, &n)
	if len(body) <= listPrefixBytes {
		t.Fatalf("test list is %d bytes; it must exceed the prefix to prove the cut is safe", len(body))
	}
	if got, ok := remainingItemCount(body[:listPrefixBytes]); !ok || got != 5132 {
		t.Errorf("protobuf prefix: got %d, %v; want 5132", got, ok)
	}
	if _, ok := remainingItemCount(protoList(t, nil)[:listPrefixBytes]); ok {
		t.Error("protobuf list without remainingItemCount reported one")
	}
	js := []byte(`{"kind":"ReplicaSetList","apiVersion":"apps/v1","metadata":{"resourceVersion":"1","continue":"x","remainingItemCount": 5132},"items":[]}`)
	if got, ok := remainingItemCount(js); !ok || got != 5132 {
		t.Errorf("JSON: got %d, %v; want 5132", got, ok)
	}
	if _, ok := remainingItemCount([]byte("garbage")); ok {
		t.Error("garbage reported a count")
	}
}

func TestListResource(t *testing.T) {
	for path, want := range map[string]string{
		"/api/v1/pods":                                "pods",
		"/api/v1/namespaces/team-a/secrets":           "secrets",
		"/apis/apps/v1/replicasets":                   "replicasets",
		"/apis/apps/v1/namespaces/team-a/deployments": "deployments",
		"/api/v1/namespaces/kube-system":              "",
		"/apis/apps/v1/namespaces/x/deployments/web":  "",
		"/version": "",
	} {
		if got := listResource(path); got != want {
			t.Errorf("listResource(%q) = %q, want %q", path, got, want)
		}
	}
}

type bodyRT struct{ body func(*http.Request) []byte }

func (b bodyRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b.body(req))), Header: http.Header{}}, nil
}

// The initial LIST of a big type: its first page says how many are left, every
// page counts, and the counting stops when the connect is over.
func TestConnectProgress_CountsTheInitialListPages(t *testing.T) {
	remaining := int64(1100)
	first := protoList(t, &remaining)
	p := newConnectProgress()
	rt := &progressRoundTripper{next: bodyRT{body: func(req *http.Request) []byte {
		if req.URL.Query().Get("continue") == "" {
			return first
		}
		return []byte("k8s\x00")
	}}, p: p}
	get := func(rawQuery string) {
		req := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/apis/apps/v1/replicasets", RawQuery: rawQuery}}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	get("limit=500")
	if got := p.listCounts()["replicasets"]; got.Loaded != 500 || got.Total != 1600 {
		t.Errorf("after page 1: %+v, want 500 of 1600", got)
	}
	get("limit=500&continue=a")
	get("limit=500&continue=b")
	get("limit=500&continue=c")
	if got := p.listCounts()["replicasets"]; got.Loaded != 1600 || got.Total != 1600 {
		t.Errorf("after the last page: %+v, want 1600 of 1600 (capped at the total)", got)
	}
	if got := p.listCounts()["replicasets"].Bytes; got != int64(len(first)+3*4) {
		t.Errorf("replicasets bytes = %d, want %d", got, len(first)+3*4)
	}
	if want := int64(len(first) + 3*4); p.listBytesReceived() != want {
		t.Errorf("bytes received = %d, want %d (every page's body)", p.listBytesReceived(), want)
	}
	// A watch, a GET of one object and a list without a limit are not pages.
	for _, u := range []*url.URL{
		{Path: "/apis/apps/v1/replicasets", RawQuery: "watch=true&limit=500"},
		{Path: "/apis/apps/v1/namespaces/x/replicasets/web", RawQuery: "limit=500"},
		{Path: "/api/v1/pods"},
	} {
		resp, _ := rt.RoundTrip(&http.Request{Method: http.MethodGet, URL: u})
		_ = resp.Body.Close()
	}
	if _, ok := p.listCounts()["pods"]; ok {
		t.Error("a list without a limit was counted")
	}
	p.listsDone.Store(true)
	get("limit=500&continue=d")
	if got := p.listCounts()["replicasets"]; got.Loaded != 1600 {
		t.Errorf("a relist after the connect was counted: %+v", got)
	}
}

// buildingConnector is a connector whose pods have synced and whose
// replicasets are still listing.
func buildingConnector(t *testing.T) *Connector {
	t.Helper()
	// The fake clientset does not stream a watch-list; the reflector must list.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	cs := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns"}})
	f := informers.NewSharedInformerFactory(cs, 0)
	f.Core().V1().Pods().Informer()
	// ReplicaSets list from a clientset of their own: a reactor that blocks
	// holds its fake's lock, which would block the Pods list too.
	slow := fake.NewClientset()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	slow.PrependReactor("list", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-release
		return true, &appsv1.ReplicaSetList{}, nil
	})
	fs := informers.NewSharedInformerFactory(slow, 0)
	fs.Apps().V1().ReplicaSets().Informer()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	f.Start(stop)
	fs.Start(stop)
	deadline := time.Now().Add(5 * time.Second)
	for !f.Core().V1().Pods().Informer().HasSynced() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	c := &Connector{factory: f, nsFactories: []informers.SharedInformerFactory{fs}, progress: newConnectProgress(),
		permissions: ResourcePermissions{"pods": {CanList: true}, "replicasets": {CanList: true}, "secrets": {CanList: false}, "vpas": {Absent: true}}}
	c.progress.lists = map[string]*initialList{"replicasets": {pages: 3, limit: 500, total: 5632}}
	return c
}

func progressCtx() context.Context {
	return WithRuntimeKey(context.Background(), RuntimeKey{Cluster: "agent:c-lab"})
}

func TestConnectProgressFor_States(t *testing.T) {
	m, pk := backoffManager(t)

	if got := m.ConnectProgressFor(progressCtx()); got.State != PrepareIdle || got.Agents != 1 || !got.ViaAgent {
		t.Errorf("nothing started: %+v, want idle with 1 agent", got)
	}

	ph := &clusterRuntime{ready: make(chan struct{}), startedAt: time.Now()}
	m.mu.Lock()
	if m.runtimes == nil {
		m.runtimes = map[poolKey]*clusterRuntime{}
	}
	m.runtimes[pk] = ph
	m.mu.Unlock()
	got := m.ConnectProgressFor(progressCtx())
	if got.State != PrepareConnecting || got.AccessChecked || got.StartedAt == nil {
		t.Errorf("probe running: %+v, want connecting, access not checked yet", got)
	}

	ph.building.Store(buildingConnector(t))
	got = m.ConnectProgressFor(progressCtx())
	if got.State != PrepareConnecting || !got.AccessChecked || got.Readable != 2 || got.Probed != 3 || len(got.Denied) != 1 || got.Denied[0] != "secrets" {
		t.Fatalf("loading: %+v, want access checked, 2 of 3 readable", got)
	}
	kinds := map[string]TypeProgress{}
	for _, tp := range got.Types {
		kinds[tp.Kind] = tp
	}
	if !kinds["Pod"].Synced || kinds["ReplicaSet"].Synced || kinds["ReplicaSet"].Loaded != 1500 || kinds["ReplicaSet"].Total != 5632 {
		t.Errorf("types: %+v, want Pod synced and ReplicaSet 1500 of 5632", got.Types)
	}

	ph.connector = &Connector{}
	close(ph.ready)
	if got := m.ConnectProgressFor(progressCtx()); got.State != PrepareReady {
		t.Errorf("built: %+v, want ready", got)
	}

	m.mu.Lock()
	delete(m.runtimes, pk)
	m.recordSpinFailureLocked(pk, errors.New("no response from the cluster for 25s"), time.Now())
	m.mu.Unlock()
	if got := m.ConnectProgressFor(progressCtx()); got.State != PrepareFailed || got.RetryAt == nil || got.Error == "" {
		t.Errorf("failed: %+v, want failed with the error and the next attempt", got)
	}
}

func TestConnectProgressFor_NoAgent(t *testing.T) {
	m := newBareManager()
	m.SetAgentRegistry(channel.NewAgentRegistry())
	if _, err := m.AddAgentProxyCluster("c-lab", "Lab"); err != nil {
		t.Fatal(err)
	}
	if got := m.ConnectProgressFor(progressCtx()); got.State != PrepareWaitingAgent {
		t.Errorf("got %+v, want waiting_agent", got)
	}
}

// The metrics scope while a cluster is prepared is its agents' cluster_id, and
// only while it is prepared.
func TestConnectingClusterIDFor_OnlyWhileBuilding(t *testing.T) {
	m, pk := backoffManager(t)
	if got := m.ConnectingClusterIDFor(progressCtx()); got != "" {
		t.Errorf("nothing building: %q", got)
	}
	ph := &clusterRuntime{ready: make(chan struct{}), startedAt: time.Now()}
	m.mu.Lock()
	if m.runtimes == nil {
		m.runtimes = map[poolKey]*clusterRuntime{}
	}
	m.runtimes[pk] = ph
	m.mu.Unlock()
	if got := m.ConnectingClusterIDFor(progressCtx()); got != "c-lab" {
		t.Errorf("building: %q, want c-lab", got)
	}
	ph.connector = &Connector{}
	close(ph.ready)
	if got := m.ConnectingClusterIDFor(progressCtx()); got != "" {
		t.Errorf("built: %q, want \"\" (the connector answers then)", got)
	}
}
