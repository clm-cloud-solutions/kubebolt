package api

import (
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

// pfLogWriter pipes client-go portforward's internal stdout/stderr
// (which are silenced today by passing nil,nil to portforward.New)
// into our structured log so we can see what k8s.io/client-go thinks
// is happening when a forward hangs. Tagged with the forward's id so
// concurrent forwards don't bleed into each other.
type pfLogWriter struct {
	id     string
	stream string // "stdout" or "stderr"
}

func (w *pfLogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	if msg == "" {
		return len(p), nil
	}
	slog.Info("client-go portforward",
		slog.String("id", w.id),
		slog.String("stream", w.stream),
		slog.String("msg", msg),
	)
	return len(p), nil
}

const maxPortForwards = 20

// PortForward represents an active port-forward session.
type PortForward struct {
	ID         string    `json:"id"`
	Namespace  string    `json:"namespace"`
	Pod        string    `json:"pod"`
	Container  string    `json:"container,omitempty"`
	RemotePort int       `json:"remotePort"`
	LocalPort  int       `json:"localPort"`
	URL        string    `json:"url"`
	Status     string    `json:"status"` // active, error, stopped
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	stopCh     chan struct{}
}

// PortForwardManager manages active port-forward sessions.
type PortForwardManager struct {
	mu       sync.RWMutex
	forwards map[string]*PortForward
}

// NewPortForwardManager creates a new manager.
func NewPortForwardManager() *PortForwardManager {
	return &PortForwardManager{
		forwards: make(map[string]*PortForward),
	}
}

// Start creates a new port-forward to a pod.
func (m *PortForwardManager) Start(conn *cluster.Connector, namespace, pod, container string, remotePort int) (*PortForward, error) {
	m.mu.Lock()
	if len(m.forwards) >= maxPortForwards {
		m.mu.Unlock()
		return nil, fmt.Errorf("maximum of %d concurrent port-forwards reached", maxPortForwards)
	}
	m.mu.Unlock()

	// Clone restConfig without timeout
	baseConfig := conn.RestConfig()
	restConfig := *baseConfig
	restConfig.Timeout = 0

	clientset := conn.Clientset()

	// Build the portforward URL
	pfURL := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("portforward").
		URL()

	// Create SPDY transport and dialer. cluster.SPDYTransportsFor
	// transparently picks the agent-proxy aware path when restConfig
	// targets an agent-proxy cluster.
	transport, upgrader, err := cluster.SPDYTransportsFor(&restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating SPDY round-tripper: %w", err)
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", pfURL)

	// Pick a free port
	localPort, err := getFreePort()
	if err != nil {
		return nil, fmt.Errorf("finding free port: %w", err)
	}

	id := uuid.New().String()[:8]
	stopCh := make(chan struct{})
	readyCh := make(chan struct{})

	pf := &PortForward{
		ID:         id,
		Namespace:  namespace,
		Pod:        pod,
		Container:  container,
		RemotePort: remotePort,
		LocalPort:  localPort,
		URL:        fmt.Sprintf(":%d", localPort),
		Status:     "starting",
		CreatedAt:  time.Now(),
		stopCh:     stopCh,
	}

	// Create the forwarder. We pipe out/errOut into structured logs
	// so client-go's internal stream-creation messages, "lost
	// connection to pod", "error copying" etc. surface in our log
	// instead of being dropped. Critical for diagnosing port-forward
	// hangs without attaching a debugger.
	ports := []string{fmt.Sprintf("%d:%d", localPort, remotePort)}
	outW := &pfLogWriter{id: id, stream: "stdout"}
	errW := &pfLogWriter{id: id, stream: "stderr"}
	fw, err := portforward.New(dialer, ports, stopCh, readyCh, outW, errW)
	if err != nil {
		return nil, fmt.Errorf("creating port forwarder: %w", err)
	}

	slog.Info("portforward starting",
		slog.String("id", id),
		slog.String("namespace", namespace),
		slog.String("pod", pod),
		slog.Int("remotePort", remotePort),
		slog.Int("localPort", localPort),
	)

	// Run in background
	go func() {
		if err := fw.ForwardPorts(); err != nil {
			m.mu.Lock()
			if existing, ok := m.forwards[id]; ok {
				existing.Status = "error"
				existing.Error = err.Error()
			}
			m.mu.Unlock()
			slog.Warn("portforward ended with error",
				slog.String("id", id),
				slog.String("error", err.Error()),
			)
		} else {
			slog.Info("portforward ended cleanly", slog.String("id", id))
		}
	}()

	// Wait for ready or timeout
	select {
	case <-readyCh:
		pf.Status = "active"
	case <-time.After(10 * time.Second):
		close(stopCh)
		return nil, fmt.Errorf("port-forward timed out waiting for ready")
	}

	m.mu.Lock()
	m.forwards[id] = pf
	m.mu.Unlock()

	log.Printf("Port-forward started: %s → %s/%s:%d (local:%d)", id, namespace, pod, remotePort, localPort)
	return pf, nil
}

// Get returns a port-forward by ID.
func (m *PortForwardManager) Get(id string) *PortForward {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.forwards[id]
}

// List returns all active port-forwards.
func (m *PortForwardManager) List() []*PortForward {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*PortForward, 0, len(m.forwards))
	for _, pf := range m.forwards {
		result = append(result, pf)
	}
	return result
}

// Stop stops a port-forward by ID.
func (m *PortForwardManager) Stop(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	pf, ok := m.forwards[id]
	if !ok {
		return false
	}
	close(pf.stopCh)
	delete(m.forwards, id)
	log.Printf("Port-forward stopped: %s", id)
	return true
}

// StopAll stops all active port-forwards (called on cluster switch).
func (m *PortForwardManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, pf := range m.forwards {
		close(pf.stopCh)
		log.Printf("Port-forward stopped (cluster switch): %s", id)
	}
	m.forwards = make(map[string]*PortForward)
}

// getFreePort asks the OS for a free port.
func getFreePort() (int, error) {
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// ─── HTTP Handlers ──────────────────────────────────────────

func (h *handlers) handleCreatePortForward(w http.ResponseWriter, r *http.Request) {
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	var body struct {
		Namespace  string `json:"namespace"`
		Pod        string `json:"pod"`
		Container  string `json:"container"`
		RemotePort int    `json:"remotePort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Namespace == "" || body.Pod == "" || body.RemotePort == 0 {
		respondError(w, http.StatusBadRequest, "namespace, pod, and remotePort are required")
		return
	}

	pf, err := h.pfManager.Start(conn, body.Namespace, body.Pod, body.Container, body.RemotePort)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, pf)
}

func (h *handlers) handleListPortForwards(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.pfManager.List())
}

func (h *handlers) handleDeletePortForward(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.pfManager.Stop(id) {
		respondError(w, http.StatusNotFound, "port-forward not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (h *handlers) handlePortForwardProxy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pf := h.pfManager.Get(id)
	if pf == nil || pf.Status != "active" {
		http.Error(w, "port-forward not found or not active", http.StatusNotFound)
		return
	}

	prefix := "/pf/" + id
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", pf.LocalPort))
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Strip the /pf/{id} prefix from the request path
	r.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}
	r.URL.RawPath = ""
	r.Host = target.Host

	// Rewrite redirect responses (301, 302, 307, 308) to stay under /pf/{id}/.
	proxy.ModifyResponse = func(resp *http.Response) error {
		if nl := rewriteUpstreamRedirect(resp.Header.Get("Location"), prefix, target.Hostname()); nl != "" {
			resp.Header.Set("Location", nl)
		}
		return nil
	}

	proxy.ServeHTTP(w, r)
}

// rewriteUpstreamRedirect rewrites a backend Location header so a redirect
// stays under the /pf/{id} prefix. It rewrites RELATIVE redirects and ABSOLUTE
// redirects that point back at the upstream host — matched by HOSTNAME,
// ignoring the port. A backend with absolute_redirect on (nginx's default)
// redirects to its own listen host, e.g. `http://127.0.0.1/login` (port 80
// omitted), which differs from our dial target's host:port
// (127.0.0.1:<localPort>); an exact host compare missed that and leaked
// 127.0.0.1 to the browser. Returns "" to mean "leave the header unchanged"
// (empty/unparseable, or an absolute redirect to a DIFFERENT host — which the
// proxy cannot know maps back into /pf/, the documented subpath limitation).
func rewriteUpstreamRedirect(loc, prefix, upstreamHost string) string {
	if loc == "" {
		return ""
	}
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	if u.Host != "" && u.Hostname() != upstreamHost {
		return ""
	}
	newPath := prefix + u.Path
	if u.RawQuery != "" {
		newPath += "?" + u.RawQuery
	}
	return newPath
}
