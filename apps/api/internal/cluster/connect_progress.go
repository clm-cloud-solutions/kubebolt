package cluster

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// maxConnectDuration caps a connect that keeps making progress. The connect and
// cache-sync timeouts are idle budgets: a connect
// fails when nothing arrives for that long, not when a slow tunnel is still
// delivering pages. A large cluster can send tens of MB through its agent at
// under 1 MB/s, which no fixed 45 s budget fits; this cap only stops a connect
// that trickles forever.
var maxConnectDuration = 10 * time.Minute

// connectProgress records when a connector last received a successful
// non-watch response from the API server: a LIST page, a discovery document,
// a GET. A watch is not progress: on a busy cluster the watches of the
// informers that already synced deliver events all the time, which would hide
// a LIST that is stuck.
type connectProgress struct {
	last atomic.Int64 // unix nanos

	// What the informers' initial LISTs have brought so far, per resource, for
	// the progress a person sees while a cluster is prepared (counted from
	// the pages as they arrive). Frozen once the connect is over:
	// a relist later is not a connect.
	listsMu   sync.Mutex
	lists     map[string]*initialList
	listsDone atomic.Bool
	// listBytes is the bytes of the initial LISTs' bodies received so far.
	listBytes atomic.Int64
}

// initialList is one resource's initial LIST so far: pages received, the page
// size asked for, and the total when the API server said it (its first page's
// remainingItemCount; a list that fits in one page never says).
type initialList struct {
	pages int
	limit int
	total int64
}

// ListCount is initialList as reported: objects received (an estimate from the
// pages, exact once the type has synced) and the total, 0 when not known.
type ListCount struct {
	Loaded int64
	Total  int64
}

func newConnectProgress() *connectProgress {
	p := &connectProgress{}
	p.touch()
	return p
}

func (p *connectProgress) touch() { p.last.Store(time.Now().UnixNano()) }

// idleFor is how long ago the last response arrived.
func (p *connectProgress) idleFor() time.Duration {
	return time.Since(time.Unix(0, p.last.Load()))
}

// progressRoundTripper marks progress on every 2xx response to a request that
// is not a watch. It sits in rest.Config.WrapTransport, so the informers, the
// discovery client and the dedicated probe/UID clients (CopyConfig keeps the
// wrapper) all report through it.
type progressRoundTripper struct {
	next http.RoundTripper
	p    *connectProgress
}

func (t *progressRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && !isWatchRequest(req) {
		t.p.touch()
		t.p.observeListPage(req, resp)
	}
	return resp, err
}

// listPrefixBytes is how much of a first LIST page is kept to read its
// remainingItemCount: the list metadata comes before the items, in protobuf
// and in JSON alike.
const listPrefixBytes = 8 << 10

// observeListPage counts a page of an informer's initial LIST (a paginated
// GET of a collection). The first page also says how many objects are left;
// its body is read through a tap that keeps its first bytes.
func (p *connectProgress) observeListPage(req *http.Request, resp *http.Response) {
	if p.listsDone.Load() || req.Method != http.MethodGet || req.URL == nil {
		return
	}
	q := req.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		return
	}
	resource := listResource(req.URL.Path)
	if resource == "" {
		return
	}
	p.listsMu.Lock()
	if p.lists == nil {
		p.lists = map[string]*initialList{}
	}
	lc := p.lists[resource]
	if lc == nil {
		lc = &initialList{}
		p.lists[resource] = lc
	}
	lc.pages++
	lc.limit = limit
	p.listsMu.Unlock()
	if resp.Body == nil {
		return
	}
	if q.Get("continue") != "" {
		resp.Body = &prefixTap{rc: resp.Body, counted: &p.listBytes}
		return
	}
	resp.Body = &prefixTap{rc: resp.Body, counted: &p.listBytes, onDone: func(prefix []byte) {
		if rest, ok := remainingItemCount(prefix); ok {
			p.listsMu.Lock()
			lc.total += int64(limit) + rest
			p.listsMu.Unlock()
		}
	}}
}

// listCounts is what the initial LISTs have brought, per resource.
func (p *connectProgress) listCounts() map[string]ListCount {
	p.listsMu.Lock()
	defer p.listsMu.Unlock()
	out := make(map[string]ListCount, len(p.lists))
	for r, lc := range p.lists {
		loaded := int64(lc.pages * lc.limit)
		if lc.total > 0 && loaded > lc.total {
			loaded = lc.total
		}
		out[r] = ListCount{Loaded: loaded, Total: lc.total}
	}
	return out
}

// listResource is the resource of a collection path — /api/v1/pods,
// /apis/apps/v1/namespaces/x/replicasets — or "" for anything else.
func listResource(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	var rest []string
	switch {
	case len(parts) >= 3 && parts[0] == "api":
		rest = parts[2:]
	case len(parts) >= 4 && parts[0] == "apis":
		rest = parts[3:]
	default:
		return ""
	}
	if len(rest) == 3 && rest[0] == "namespaces" {
		rest = rest[2:]
	}
	if len(rest) != 1 {
		return ""
	}
	return rest[0]
}

// listBytesReceived is the bytes of the initial LISTs received so far.
func (p *connectProgress) listBytesReceived() int64 { return p.listBytes.Load() }

// prefixTap passes a body through, adds what it reads to counted, and hands
// its first listPrefixBytes to onDone (when set) once, when they are in or
// the body ends.
type prefixTap struct {
	rc      io.ReadCloser
	buf     bytes.Buffer
	done    bool
	counted *atomic.Int64
	onDone  func([]byte)
}

func (t *prefixTap) Read(b []byte) (int, error) {
	n, err := t.rc.Read(b)
	if t.counted != nil && n > 0 {
		t.counted.Add(int64(n))
	}
	if t.onDone == nil {
		return n, err
	}
	if !t.done {
		if room := listPrefixBytes - t.buf.Len(); room > 0 {
			if n < room {
				room = n
			}
			t.buf.Write(b[:room])
		}
		if t.buf.Len() >= listPrefixBytes || err != nil {
			t.finish()
		}
	}
	return n, err
}

func (t *prefixTap) Close() error {
	t.finish()
	return t.rc.Close()
}

func (t *prefixTap) finish() {
	if t.done || t.onDone == nil {
		return
	}
	t.done = true
	t.onDone(t.buf.Bytes())
}

var jsonRemaining = regexp.MustCompile(`"remainingItemCount"\s*:\s*(\d+)`)

// k8sProtoMagic starts every Kubernetes protobuf body.
var k8sProtoMagic = []byte("k8s\x00")

// remainingItemCount reads a list's metadata.remainingItemCount from the
// start of its body: protobuf (runtime.Unknown → raw → ListMeta field 4) or
// JSON. ok is false when the list did not say (it fits in one page).
func remainingItemCount(prefix []byte) (int64, bool) {
	if bytes.HasPrefix(prefix, k8sProtoMagic) {
		return protoRemaining(prefix[len(k8sProtoMagic):])
	}
	m := jsonRemaining.FindSubmatch(prefix)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(string(m[1]), 10, 64)
	return n, err == nil
}

// protoRemaining walks runtime.Unknown { typeMeta = 1; raw = 2; ... } to raw,
// the list message, whose field 1 is its ListMeta { ...; remainingItemCount = 4 }.
// The prefix may cut raw short; ListMeta, first and small, is inside it.
func protoRemaining(b []byte) (int64, bool) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return 0, false
		}
		b = b[n:]
		if num == 2 && typ == protowire.BytesType {
			l, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return 0, false
			}
			raw := b[n:]
			if uint64(len(raw)) > l {
				raw = raw[:l]
			}
			return listMetaRemaining(raw)
		}
		n = protowire.ConsumeFieldValue(num, typ, b)
		if n < 0 {
			return 0, false
		}
		b = b[n:]
	}
	return 0, false
}

func listMetaRemaining(raw []byte) (int64, bool) {
	num, typ, n := protowire.ConsumeTag(raw)
	if n < 0 || num != 1 || typ != protowire.BytesType {
		return 0, false
	}
	meta, n := protowire.ConsumeBytes(raw[n:])
	if n < 0 {
		return 0, false
	}
	for len(meta) > 0 {
		num, typ, n := protowire.ConsumeTag(meta)
		if n < 0 {
			return 0, false
		}
		meta = meta[n:]
		if num == 4 && typ == protowire.VarintType {
			v, n := protowire.ConsumeVarint(meta)
			if n < 0 {
				return 0, false
			}
			return int64(v), true
		}
		n = protowire.ConsumeFieldValue(num, typ, meta)
		if n < 0 {
			return 0, false
		}
		meta = meta[n:]
	}
	return 0, false
}

// WrappedRoundTripper lets client-go's transport helpers reach the transport
// underneath (utilnet.RoundTripperWrapper).
func (t *progressRoundTripper) WrappedRoundTripper() http.RoundTripper { return t.next }

func isWatchRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	switch req.URL.Query().Get("watch") {
	case "true", "1":
		return true
	}
	return false
}

// connectBudgetError says which budget ended a connect: no response for the
// idle budget (stalled), or the overall cap.
type connectBudgetError struct {
	stalled bool
	budget  time.Duration
}

func (e *connectBudgetError) Error() string {
	if e.stalled {
		return "no response from the cluster for " + e.budget.String()
	}
	return "still connecting after " + e.budget.String()
}

// progressContext is canceled when p sees no progress for idle, or when the
// connect has run for maxTotal, whichever comes first. The cause is available
// from context.Cause. idle <= 0 disables the idle budget (the cap still holds).
func progressContext(parent context.Context, p *connectProgress, idle, maxTotal time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	tick := idle / 10
	if tick < 50*time.Millisecond {
		tick = 50 * time.Millisecond
	}
	if tick > time.Second {
		tick = time.Second
	}
	start := time.Now()
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if maxTotal > 0 && time.Since(start) >= maxTotal {
					cancel(&connectBudgetError{budget: maxTotal})
					return
				}
				if idle > 0 && p.idleFor() >= idle {
					cancel(&connectBudgetError{stalled: true, budget: idle})
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(context.Canceled) }
}
