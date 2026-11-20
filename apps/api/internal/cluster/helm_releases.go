package cluster

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/kubebolt/kubebolt/apps/api/internal/helm"
)

// Helm's release storage, read on demand.
//
// Helm keeps every revision of every release as a Secret of type
// helm.sh/release.v1 whose one value is the whole release, gzipped: the chart,
// the values and every manifest it rendered. Only the release detail view needs
// that history, yet the Secrets informer downloaded all of it at connect and
// kept it in memory, and the insight tick and the Overview listed it again,
// payload included, every minute. Now:
//
//   - the Secrets informer leaves the type out (registerSecretInformer);
//   - what lists and counts need — names, labels, timestamps — comes from a
//     metadata-only LIST, refreshed at most once a minute (helmMetaTTL);
//   - a revision's payload is read when something needs it: the latest
//     revision of a release for the Applications list or for an insight about
//     it, remembered by resourceVersion; the detail view reads one release.
//
// A metadata informer would watch instead of listing, but the agent relays a
// watch as plain JSON (packages/agent kube_proxy.go), which turns a metadata
// watch into full objects. A metadata LIST is a unary request and keeps its
// Accept header through the tunnel.

// helmReleaseSecretType is the type Helm 3 gives its release storage Secrets.
const helmReleaseSecretType = "helm.sh/release.v1"

// helmReleaseSecretPrefix starts every release storage Secret's name:
// sh.helm.release.v1.<release>.v<revision>.
const helmReleaseSecretPrefix = "sh.helm.release.v1."

// helmMetaTTL is how long a metadata LIST of the release storage serves before
// a background refresh. It matches the insight tick, the most frequent reader.
const helmMetaTTL = 60 * time.Second

// helmReadTimeout bounds a request-path read of the release metadata when the
// cache is cold.
const helmReadTimeout = 5 * time.Second

// helmFetchConcurrency is how many release payloads are read at once by GET.
const helmFetchConcurrency = 4

// helmGetLimit is how many unread revisions are read by GET. A list never
// costs more requests than the one LIST it replaced: one release that changed
// since the last view is one GET; more — the first Applications view — are one
// LIST of the revisions Helm has not superseded, each release's latest and
// never its history (helmLatestPageSize per page). Per-object reads are for
// the detail views.
const helmGetLimit = 1

// helmLatestPageSize keeps a page of release payloads (up to 1 MiB each, the
// Secret size limit) well under what the agent relays in one response.
const helmLatestPageSize = 8

// registerSecretInformer installs f's Secrets informer without Helm's release
// storage, before anything asks the factory for it: the typed accessor
// (f.Core().V1().Secrets()) then returns this informer. It keeps the factory's
// shared options: managedFields stripped (the factory's transform, applied by
// InformerFor) and the initial LIST paginated (listFromServerNotWatchCache,
// which a custom informer has to apply itself).
func registerSecretInformer(f informers.SharedInformerFactory, namespace string) {
	f.InformerFor(&corev1.Secret{}, func(cs kubernetes.Interface, resync time.Duration) cache.SharedIndexInformer {
		return coreinformers.NewFilteredSecretInformer(cs, namespace, resync,
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			secretListOptions)
	})
}

// secretListOptions is the Secrets informer's LIST and WATCH tweak.
func secretListOptions(opts *metav1.ListOptions) {
	listFromServerNotWatchCache(opts)
	opts.FieldSelector = withoutHelmReleases(opts.FieldSelector)
}

// withoutHelmReleases adds the exclusion of Helm's storage type to a field
// selector. The metadata LIST selects exactly that type, so the two partition
// the Secrets: none is lost and none counted twice.
func withoutHelmReleases(sel string) string {
	not := "type!=" + helmReleaseSecretType
	if sel == "" {
		return not
	}
	return sel + "," + not
}

// helmSecretMeta is one revision's storage Secret, as metadata.
type helmSecretMeta struct {
	meta     metav1.ObjectMeta // managedFields dropped
	release  string            // label name
	status   string            // label status
	revision int               // label version
}

func helmMetaOf(o *metav1.PartialObjectMetadata) helmSecretMeta {
	m := o.ObjectMeta
	m.ManagedFields = nil
	rev, _ := strconv.Atoi(m.Labels["version"])
	return helmSecretMeta{meta: m, release: m.Labels["name"], status: m.Labels["status"], revision: rev}
}

// helmMemo is a decoded summary and the resourceVersion it was decoded at.
type helmMemo struct {
	rv  string
	rel helm.Release
}

// helmReleaseCache holds the release storage metadata and the summaries of
// the revisions read so far.
type helmReleaseCache struct {
	mu        sync.Mutex
	metas     []helmSecretMeta
	checkedAt time.Time
	fetching  bool
	lastErr   error
	memo      map[types.UID]helmMemo
}

// helmMetas returns the release storage Secrets as metadata. A fresh cache is
// served as is; a stale one is served while a refresh runs in the background.
// A cold one is read now when wait is true (bounded by ctx), else warmed in the
// background and reported empty.
func (c *Connector) helmMetas(ctx context.Context, wait bool) ([]helmSecretMeta, error) {
	h := &c.helm
	h.mu.Lock()
	if !h.checkedAt.IsZero() {
		metas, err := h.metas, h.lastErr
		if !h.fetching && time.Since(h.checkedAt) > helmMetaTTL {
			h.fetching = true
			go c.refreshHelmMetas()
		}
		h.mu.Unlock()
		if metas == nil {
			return nil, err
		}
		return metas, nil
	}
	if !wait {
		if !h.fetching {
			h.fetching = true
			go c.refreshHelmMetas()
		}
		h.mu.Unlock()
		return nil, nil
	}
	h.mu.Unlock()

	metas, err := c.listHelmMetas(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		// A refusal is an answer: keep it for the TTL like a success. Anything
		// else (a timeout, a tunnel blip) leaves the cache cold so the next
		// reader tries again.
		if apierrors.IsForbidden(err) {
			h.checkedAt, h.lastErr = time.Now(), err
		}
		return nil, err
	}
	c.storeHelmMetasLocked(metas)
	return metas, nil
}

// warmHelmMetas starts a background read of the release metadata unless one
// is running or the cache is fresh. Start calls it so the Overview's counts
// do not wait for the first reader.
func (c *Connector) warmHelmMetas() {
	h := &c.helm
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fetching || (!h.checkedAt.IsZero() && time.Since(h.checkedAt) <= helmMetaTTL) {
		return
	}
	h.fetching = true
	go c.refreshHelmMetas()
}

// refreshHelmMetas re-reads the release metadata off the request path. A
// failed read keeps what was there: a release does not vanish from the list
// because one minute's LIST failed.
func (c *Connector) refreshHelmMetas() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	metas, err := c.listHelmMetas(ctx)
	h := &c.helm
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fetching = false
	h.checkedAt = time.Now()
	if err != nil {
		h.lastErr = err
		return
	}
	c.storeHelmMetasLocked(metas)
}

// storeHelmMetasLocked replaces the metadata and forgets the summaries of
// revisions that are no longer the latest of their release. Caller holds
// c.helm.mu.
func (c *Connector) storeHelmMetasLocked(metas []helmSecretMeta) {
	h := &c.helm
	h.metas, h.checkedAt, h.lastErr = metas, time.Now(), nil
	if metas == nil {
		h.metas = []helmSecretMeta{}
	}
	keep := make(map[types.UID]struct{})
	for _, m := range latestHelmRevisions(metas) {
		keep[m.meta.UID] = struct{}{}
	}
	for uid := range h.memo {
		if _, ok := keep[uid]; !ok {
			delete(h.memo, uid)
		}
	}
}

// listHelmMetas lists the release storage Secrets' metadata — kilobytes, where
// the Secrets themselves weigh what every chart, value and manifest of every
// revision weighs. A namespace-scoped connection lists the namespaces it may
// read, like its Secrets informers.
func (c *Connector) listHelmMetas(ctx context.Context) ([]helmSecretMeta, error) {
	if c.metadataClient == nil {
		return nil, fmt.Errorf("no metadata client")
	}
	res := c.metadataClient.Resource(corev1.SchemeGroupVersion.WithResource("secrets"))
	var out []helmSecretMeta
	for _, ns := range c.helmNamespaces() {
		opts := metav1.ListOptions{FieldSelector: "type=" + helmReleaseSecretType, Limit: 500}
		for {
			list, err := res.Namespace(ns).List(ctx, opts)
			if err != nil {
				return nil, err
			}
			for i := range list.Items {
				out = append(out, helmMetaOf(&list.Items[i]))
			}
			if list.Continue == "" {
				break
			}
			opts.Continue = list.Continue
		}
	}
	return out, nil
}

// helmNamespaces is where the release storage is read: everywhere, or the
// namespaces a namespace-scoped connection may read Secrets in.
func (c *Connector) helmNamespaces() []string {
	if p, ok := c.Permissions()["secrets"]; ok && p != nil && p.NamespaceScoped && len(p.Namespaces) > 0 {
		return p.Namespaces
	}
	return []string{metav1.NamespaceAll}
}

// latestHelmRevisions keeps the highest revision of each release. A storage
// Secret without Helm's name label is not a release Helm wrote, and is left out.
func latestHelmRevisions(metas []helmSecretMeta) []helmSecretMeta {
	latest := make(map[string]helmSecretMeta)
	for _, m := range metas {
		if m.release == "" {
			continue
		}
		key := m.meta.Namespace + "/" + m.release
		if cur, ok := latest[key]; !ok || m.revision > cur.revision {
			latest[key] = m
		}
	}
	out := make([]helmSecretMeta, 0, len(latest))
	for _, m := range latest {
		out = append(out, m)
	}
	return out
}

// HelmReleases returns the latest revision of every release, decoded — the
// Applications list. A revision's storage is read the first time it is the
// latest, and not again until it changes; the history is never read here.
// It returns what it could decode alongside the first error it met.
func (c *Connector) HelmReleases(ctx context.Context) ([]helm.Release, error) {
	return c.HelmReleasesWithStatus(ctx, nil)
}

// HelmReleasesWithStatus is HelmReleases for the releases whose latest
// revision's status satisfies keep (nil keeps all). The others' storage is not
// read: the status is a label Helm keeps in step with the payload.
func (c *Connector) HelmReleasesWithStatus(ctx context.Context, keep func(status string) bool) ([]helm.Release, error) {
	metas, err := c.helmMetas(ctx, true)
	if err != nil {
		return nil, err
	}
	var wanted []helmSecretMeta
	for _, m := range latestHelmRevisions(metas) {
		if keep == nil || keep(m.status) {
			wanted = append(wanted, m)
		}
	}
	return c.resolveHelmReleases(ctx, wanted)
}

// resolveHelmReleases decodes the given revisions, reading from the cluster
// only those not decoded at their current resourceVersion. A read that fails
// falls back to an older decode of the same Secret when there is one, so an
// insight about it does not resolve because one GET failed.
func (c *Connector) resolveHelmReleases(ctx context.Context, wanted []helmSecretMeta) ([]helm.Release, error) {
	h := &c.helm
	out := make([]helm.Release, 0, len(wanted))
	var missing []helmSecretMeta
	h.mu.Lock()
	for _, m := range wanted {
		if e, ok := h.memo[m.meta.UID]; ok && e.rv == m.meta.ResourceVersion {
			out = append(out, e.rel)
			continue
		}
		missing = append(missing, m)
	}
	h.mu.Unlock()

	if len(missing) > helmGetLimit {
		read := c.listUnsupersededHelmRevisions(ctx, missing)
		rest := missing[:0:0]
		for _, m := range missing {
			if rel, ok := read[m.meta.UID]; ok {
				out = append(out, rel)
				continue
			}
			rest = append(rest, m)
		}
		missing = rest
	}

	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
		sem      = make(chan struct{}, helmFetchConcurrency)
	)
	for _, m := range missing {
		wg.Add(1)
		sem <- struct{}{}
		go func(m helmSecretMeta) {
			defer wg.Done()
			defer func() { <-sem }()
			rel, rv, ok, err := c.readHelmRevision(ctx, m)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				h.mu.Lock()
				e, had := h.memo[m.meta.UID]
				h.mu.Unlock()
				if had {
					out = append(out, e.rel)
				}
				return
			}
			if !ok {
				return
			}
			h.mu.Lock()
			if h.memo == nil {
				h.memo = make(map[types.UID]helmMemo)
			}
			h.memo[m.meta.UID] = helmMemo{rv: rv, rel: rel}
			h.mu.Unlock()
			out = append(out, rel)
		}(m)
	}
	wg.Wait()
	if firstErr != nil {
		log.Printf("helm: %d of %d release revisions could not be read on %s: %v", unreadCount(missing, out), len(missing), c.clusterName, firstErr)
	}
	helm.SortReleases(out)
	return out, firstErr
}

// unreadCount is how many of the missing revisions did not make it into out —
// for the log line only.
func unreadCount(missing []helmSecretMeta, out []helm.Release) int {
	got := make(map[string]struct{}, len(out))
	for _, r := range out {
		got[r.Namespace+"/"+r.Name] = struct{}{}
	}
	n := 0
	for _, m := range missing {
		if _, ok := got[m.meta.Namespace+"/"+m.release]; !ok {
			n++
		}
	}
	return n
}

// listUnsupersededHelmRevisions reads, in one paginated LIST per namespace
// scope, the release storage Secrets Helm has not marked superseded — each
// release's latest revision, plus any older failed or pending one — and decodes
// those in want, remembering them. History (superseded revisions) is never
// sent. What it cannot read is left to the GETs; its error is not reported
// because they will meet it again.
func (c *Connector) listUnsupersededHelmRevisions(ctx context.Context, want []helmSecretMeta) map[types.UID]helm.Release {
	got := make(map[types.UID]helm.Release, len(want))
	if c.clientset == nil {
		return got
	}
	wanted := make(map[types.UID]struct{}, len(want))
	for _, m := range want {
		wanted[m.meta.UID] = struct{}{}
	}
	h := &c.helm
	for _, ns := range c.helmNamespaces() {
		opts := metav1.ListOptions{
			LabelSelector: "owner=helm,status!=superseded",
			FieldSelector: "type=" + helmReleaseSecretType,
			Limit:         helmLatestPageSize,
		}
		for {
			list, err := c.clientset.CoreV1().Secrets(ns).List(ctx, opts)
			if err != nil {
				return got
			}
			for i := range list.Items {
				sec := &list.Items[i]
				if _, ok := wanted[sec.UID]; !ok {
					continue
				}
				rel, ok := helm.DecodeSummary(sec)
				if !ok {
					continue
				}
				got[sec.UID] = rel
				h.mu.Lock()
				if h.memo == nil {
					h.memo = make(map[types.UID]helmMemo)
				}
				h.memo[sec.UID] = helmMemo{rv: sec.ResourceVersion, rel: rel}
				h.mu.Unlock()
			}
			if list.Continue == "" {
				break
			}
			opts.Continue = list.Continue
		}
	}
	return got
}

// readHelmRevision reads one revision's storage Secret and decodes it. ok is
// false when the Secret is gone or holds nothing decodable — not an error.
func (c *Connector) readHelmRevision(ctx context.Context, m helmSecretMeta) (helm.Release, string, bool, error) {
	if c.clientset == nil {
		return helm.Release{}, "", false, fmt.Errorf("no clientset")
	}
	sec, err := c.clientset.CoreV1().Secrets(m.meta.Namespace).Get(ctx, m.meta.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return helm.Release{}, "", false, nil
		}
		return helm.Release{}, "", false, err
	}
	rel, ok := helm.DecodeSummary(sec)
	return rel, sec.ResourceVersion, ok, nil
}

// helmReleaseCountCached counts distinct releases from the metadata; never a
// read on the Overview's path (cold → 0 now, the count on the next refresh).
func (c *Connector) helmReleaseCountCached() int {
	metas, _ := c.helmMetas(context.Background(), false)
	return len(latestHelmRevisions(metas))
}

// helmSecretCountCached is how many release storage Secrets there are, for the
// Overview's Secrets count, which counted them when the informer held them.
func (c *Connector) helmSecretCountCached() int {
	metas, _ := c.helmMetas(context.Background(), false)
	return len(metas)
}

// helmSecretRows returns the release storage Secrets as rows of the Secrets
// list, built from metadata: the list shows names, types, keys and labels,
// never values, and Helm's storage always has one key, "release".
func (c *Connector) helmSecretRows(namespace string) []map[string]interface{} {
	ctx, cancel := context.WithTimeout(context.Background(), helmReadTimeout)
	defer cancel()
	metas, _ := c.helmMetas(ctx, true)
	var rows []map[string]interface{}
	for _, m := range metas {
		if namespace != "" && m.meta.Namespace != namespace {
			continue
		}
		rows = append(rows, helmSecretToMap(m))
	}
	return rows
}

// helmSecretDetail returns a release storage Secret's row by name, for a
// detail lookup the Secrets informer no longer answers.
func (c *Connector) helmSecretDetail(namespace, name string) (map[string]interface{}, bool) {
	if !strings.HasPrefix(name, helmReleaseSecretPrefix) {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), helmReadTimeout)
	defer cancel()
	metas, _ := c.helmMetas(ctx, true)
	for _, m := range metas {
		if m.meta.Namespace == namespace && m.meta.Name == name {
			return helmSecretToMap(m), true
		}
	}
	return nil, false
}

// helmSecretToMap is secretToMap for a release storage Secret known by its
// metadata.
func helmSecretToMap(m helmSecretMeta) map[string]interface{} {
	return map[string]interface{}{
		"name":            m.meta.Name,
		"namespace":       m.meta.Namespace,
		"status":          "Active",
		"type":            helmReleaseSecretType,
		"keys":            []string{"release"},
		"dataCount":       1,
		"labels":          safeLabels(m.meta.Labels),
		"annotations":     safeAnnotations(m.meta.Annotations),
		"createdAt":       m.meta.CreationTimestamp.Time.Format(time.RFC3339),
		"age":             formatAge(m.meta.CreationTimestamp.Time),
		"ownerReferences": ownerRefsToSlice(m.meta.OwnerReferences),
	}
}
