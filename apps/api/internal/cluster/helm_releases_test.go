package cluster

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/insights"
)

// helmRevision is one revision of a release as Helm stores it: the payload
// Secret the cluster serves and the metadata the API lists.
type helmRevision struct {
	ns, release, status, chartVersion string
	rev                               int
}

func (r helmRevision) secretName() string {
	return fmt.Sprintf("%s%s.v%d", helmReleaseSecretPrefix, r.release, r.rev)
}

func (r helmRevision) uid() types.UID {
	return types.UID(r.ns + "/" + r.secretName())
}

func (r helmRevision) labels() map[string]string {
	return map[string]string{"name": r.release, "owner": "helm", "status": r.status, "version": fmt.Sprint(r.rev)}
}

func (r helmRevision) secret(t *testing.T, rv string) *corev1.Secret {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"name": r.release, "namespace": r.ns, "version": r.rev,
		"info": map[string]any{"status": r.status, "description": "rev " + fmt.Sprint(r.rev),
			"last_deployed": time.Date(2026, 11, 1, 0, 0, r.rev, 0, time.UTC)},
		"chart": map[string]any{"metadata": map[string]any{"name": r.release + "-chart", "version": r.chartVersion}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(payload)
	_ = w.Close()
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: r.secretName(), Namespace: r.ns, UID: r.uid(), ResourceVersion: rv, Labels: r.labels()},
		Type:       helmReleaseSecretType,
		Data:       map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(gz.Bytes()))},
	}
}

func (r helmRevision) meta(rv string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: r.secretName(), Namespace: r.ns, UID: r.uid(), ResourceVersion: rv, Labels: r.labels()},
	}
}

// helmTestConnector serves revs as payload Secrets (clientset) and as metadata
// (metadata client), both at resourceVersion "1".
func helmTestConnector(t *testing.T, revs ...helmRevision) (*Connector, *fake.Clientset, *metadatafake.FakeMetadataClient) {
	t.Helper()
	var secrets []runtime.Object
	var metas []runtime.Object
	for _, r := range revs {
		secrets = append(secrets, r.secret(t, "1"))
		metas = append(metas, r.meta("1"))
	}
	scheme := metadatafake.NewTestScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mc := metadatafake.NewSimpleMetadataClient(scheme, metas...)
	cs := fake.NewClientset(secrets...)
	return &Connector{clientset: cs, metadataClient: mc, clusterName: "test"}, cs, mc
}

func secretGets(cs *fake.Clientset) []string {
	var out []string
	for _, a := range cs.Actions() {
		if a.GetResource().Resource != "secrets" {
			continue
		}
		switch a := a.(type) {
		case k8stesting.GetAction:
			out = append(out, a.GetNamespace()+"/"+a.GetName())
		case k8stesting.ListAction:
			out = append(out, "LIST "+a.GetNamespace())
		}
	}
	return out
}

var kubePromStack = []helmRevision{
	{ns: "monitoring", release: "kps", status: "superseded", chartVersion: "60.0.0", rev: 1},
	{ns: "monitoring", release: "kps", status: "superseded", chartVersion: "61.0.0", rev: 2},
	{ns: "monitoring", release: "kps", status: "deployed", chartVersion: "62.0.0", rev: 3},
}

var brokenRelease = helmRevision{ns: "shop", release: "cart", status: "failed", chartVersion: "1.2.0", rev: 1}

// The list reads the latest revision of each release, once, in one request;
// the history is never read.
func TestHelmReleases_ReadsEachLatestRevisionOnce(t *testing.T) {
	c, cs, _ := helmTestConnector(t, append(kubePromStack, brokenRelease)...)

	got, err := c.HelmReleases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d releases, want 2: %+v", len(got), got)
	}
	if got[0].Name != "kps" || got[0].Revision != 3 || got[0].ChartVersion != "62.0.0" || got[0].Chart != "kps-chart" || got[0].Status != "deployed" {
		t.Errorf("kps = %+v, want revision 3 of chart kps-chart 62.0.0, deployed", got[0])
	}
	if got[1].Name != "cart" || got[1].Status != "failed" || got[1].Description != "rev 1" {
		t.Errorf("cart = %+v, want the failed revision with its description", got[1])
	}
	// Two unread releases: one LIST of the unsuperseded revisions, no GET.
	if reads := secretGets(cs); len(reads) != 1 || !strings.HasPrefix(reads[0], "LIST") {
		t.Errorf("payload reads = %v, want one LIST", reads)
	}

	cs.ClearActions()
	if _, err := c.HelmReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reads := secretGets(cs); len(reads) != 0 {
		t.Errorf("second list read %v; a revision already decoded at its resourceVersion is not read again", reads)
	}
}

// A revision that changes (Helm moves it from pending-upgrade to deployed) is
// read again; one that does not is not.
func TestHelmReleases_RereadsARevisionWhenItChanges(t *testing.T) {
	c, cs, mc := helmTestConnector(t, append(kubePromStack, brokenRelease)...)
	if _, err := c.HelmReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	gvr := corev1.SchemeGroupVersion.WithResource("secrets")
	if err := mc.Tracker().Update(gvr, brokenRelease.meta("2"), "shop"); err != nil {
		t.Fatal(err)
	}
	if err := cs.Tracker().Update(gvr, brokenRelease.secret(t, "2"), "shop"); err != nil {
		t.Fatal(err)
	}
	expireHelmMetas(c)
	cs.ClearActions()

	if _, err := c.HelmReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reads := secretGets(cs); len(reads) != 1 || reads[0] != "shop/"+brokenRelease.secretName() {
		t.Errorf("payload reads = %v, want only the changed revision", reads)
	}
}

// The insight tick decodes only the releases a Helm rule reports on.
func TestHelmReleasesWithStatus_DecodesOnlyWhatARuleReads(t *testing.T) {
	c, cs, _ := helmTestConnector(t, append(kubePromStack, brokenRelease)...)
	got, err := c.HelmReleasesWithStatus(context.Background(), insights.HelmStatusNeedsAttention)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "cart" {
		t.Fatalf("got %+v, want only the failed release", got)
	}
	if reads := secretGets(cs); len(reads) != 1 || reads[0] != "shop/"+brokenRelease.secretName() {
		t.Errorf("payload reads = %v, want only the failed release's latest revision", reads)
	}
}

// A re-read that fails keeps the last decode of the same Secret: the failed
// release stays reported instead of resolving because one GET failed.
func TestHelmReleases_KeepsTheLastDecodeWhenARereadFails(t *testing.T) {
	c, cs, mc := helmTestConnector(t, brokenRelease)
	if _, err := c.HelmReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	gvr := corev1.SchemeGroupVersion.WithResource("secrets")
	if err := mc.Tracker().Update(gvr, brokenRelease.meta("2"), "shop"); err != nil {
		t.Fatal(err)
	}
	expireHelmMetas(c)
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("agent tunnel closed")
	})

	got, err := c.HelmReleases(context.Background())
	if err == nil {
		t.Error("want the read error reported alongside the result")
	}
	if len(got) != 1 || got[0].Name != "cart" || got[0].Status != "failed" {
		t.Errorf("got %+v, want the last decode of cart", got)
	}
}

// A refusal to list is the list's answer (403 on the Applications page), and
// it is not asked again on every request within the TTL.
func TestHelmReleases_ForbiddenIsReportedAndRemembered(t *testing.T) {
	c, _, mc := helmTestConnector(t, brokenRelease)
	lists := 0
	mc.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", fmt.Errorf("no"))
	})
	for i := 0; i < 3; i++ {
		if _, err := c.HelmReleases(context.Background()); !apierrors.IsForbidden(err) {
			t.Fatalf("call %d: err = %v, want Forbidden", i, err)
		}
	}
	if lists != 1 {
		t.Errorf("listed %d times, want 1 within the TTL", lists)
	}
}

// A release that is gone from the cluster is forgotten with its decode.
func TestHelmReleases_ForgetsAReleaseThatIsGone(t *testing.T) {
	c, _, mc := helmTestConnector(t, append(kubePromStack, brokenRelease)...)
	if _, err := c.HelmReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	gvr := corev1.SchemeGroupVersion.WithResource("secrets")
	if err := mc.Tracker().Delete(gvr, "shop", brokenRelease.secretName()); err != nil {
		t.Fatal(err)
	}
	expireHelmMetas(c)
	got, err := c.HelmReleases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "kps" {
		t.Errorf("got %+v, want only kps", got)
	}
	c.helm.mu.Lock()
	defer c.helm.mu.Unlock()
	if _, ok := c.helm.memo[brokenRelease.uid()]; ok {
		t.Error("the deleted release's decode is still remembered")
	}
}

// The Secrets list, the detail lookup and the counts still show Helm's
// storage, from metadata, without its payload.
func TestSecretsViews_IncludeHelmStorageFromMetadata(t *testing.T) {
	c, cs, _ := helmTestConnector(t, append(kubePromStack, brokenRelease)...)
	rows := c.helmSecretRows("monitoring")
	if len(rows) != 3 {
		t.Fatalf("monitoring rows = %d, want the 3 kps revisions", len(rows))
	}
	for _, r := range rows {
		if r["type"] != helmReleaseSecretType || r["dataCount"] != 1 {
			t.Errorf("row %v: want type %s with one key", r, helmReleaseSecretType)
		}
	}
	if row, ok := c.helmSecretDetail("shop", brokenRelease.secretName()); !ok || row["name"] != brokenRelease.secretName() {
		t.Errorf("detail = %v, %v; want the cart revision", row, ok)
	}
	if _, ok := c.helmSecretDetail("shop", "db-password"); ok {
		t.Error("a name outside Helm's storage must not be looked up there")
	}
	if n := c.helmSecretCountCached(); n != 4 {
		t.Errorf("storage count = %d, want 4", n)
	}
	if n := c.helmReleaseCountCached(); n != 2 {
		t.Errorf("release count = %d, want 2", n)
	}
	if reads := secretGets(cs); len(reads) != 0 {
		t.Errorf("payload reads = %v; the Secrets views read metadata only", reads)
	}
}

// The Secrets informer leaves Helm's storage out, keeps the paginated initial
// LIST, and is the one the typed accessor (and so the lister) returns.
func TestRegisterSecretInformer_LeavesHelmStorageOut(t *testing.T) {
	for _, ns := range []string{metav1.NamespaceAll, "team-a"} {
		cs := fake.NewClientset()
		var seen []metav1.ListOptions
		cs.PrependReactor("list", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			seen = append(seen, a.(k8stesting.ListActionImpl).ListOptions)
			return false, nil, nil
		})
		f := newInformerFactory(cs, informers.WithNamespace(ns))
		registerSecretInformer(f, ns)
		inf := f.Core().V1().Secrets().Informer()
		ctx, cancel := context.WithCancel(context.Background())
		f.Start(ctx.Done())
		f.WaitForCacheSync(ctx.Done())
		cancel()
		f.Shutdown()

		if len(seen) == 0 {
			t.Fatalf("ns %q: the informer never listed", ns)
		}
		if !strings.Contains(seen[0].FieldSelector, "type!="+helmReleaseSecretType) {
			t.Errorf("ns %q: field selector %q does not leave Helm's storage out", ns, seen[0].FieldSelector)
		}
		if seen[0].ResourceVersion == "0" && seen[0].Limit > 0 {
			t.Errorf("ns %q: initial LIST kept resourceVersion=0, so it would not paginate", ns)
		}
		if inf != f.InformerFor(&corev1.Secret{}, nil) {
			t.Errorf("ns %q: the typed accessor returned another informer", ns)
		}
	}
}

func TestWithoutHelmReleases(t *testing.T) {
	if got := withoutHelmReleases(""); got != "type!=helm.sh/release.v1" {
		t.Errorf("empty: %q", got)
	}
	if got := withoutHelmReleases("metadata.name=x"); got != "metadata.name=x,type!=helm.sh/release.v1" {
		t.Errorf("composed: %q", got)
	}
}

// expireHelmMetas makes the next read list again, synchronously.
func expireHelmMetas(c *Connector) {
	c.helm.mu.Lock()
	c.helm.checkedAt = time.Time{}
	c.helm.mu.Unlock()
}

func containsAll(have, want []string) bool {
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// The first view of many releases reads them in one LIST of the revisions
// Helm has not superseded — each release's latest, never the history — not one
// GET per release.
func TestHelmReleases_ManyUnreadReleasesAreReadInOneList(t *testing.T) {
	revs := append([]helmRevision{}, kubePromStack...)
	revs = append(revs, brokenRelease,
		helmRevision{ns: "kube-system", release: "cilium", status: "deployed", chartVersion: "1.19.1", rev: 2},
		helmRevision{ns: "kube-system", release: "cilium", status: "superseded", chartVersion: "1.19.0", rev: 1},
		helmRevision{ns: "falco", release: "falco", status: "deployed", chartVersion: "9.1.0", rev: 1},
	)
	c, cs, _ := helmTestConnector(t, revs...)
	got, err := c.HelmReleases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d releases, want 4: %+v", len(got), got)
	}
	for _, r := range got {
		if r.Chart == "" || r.ChartVersion == "" {
			t.Errorf("%s/%s decoded without its chart: %+v", r.Namespace, r.Name, r)
		}
	}
	var lists []string
	gets := 0
	for _, a := range cs.Actions() {
		if a.GetResource().Resource != "secrets" {
			continue
		}
		switch a := a.(type) {
		case k8stesting.ListActionImpl:
			lists = append(lists, a.ListOptions.LabelSelector)
		case k8stesting.GetAction:
			gets++
		}
	}
	if len(lists) != 1 || !strings.Contains(lists[0], "status!=superseded") || gets != 0 {
		t.Errorf("reads: lists %q, gets %d; want one LIST of unsuperseded revisions and no GET", lists, gets)
	}

	cs.ClearActions()
	if _, err := c.HelmReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reads := secretGets(cs); len(reads) != 0 {
		t.Errorf("second view read %v", reads)
	}
}
