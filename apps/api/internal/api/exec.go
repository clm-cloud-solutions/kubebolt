package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	gorilla "github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

type execMessage struct {
	Type    string `json:"type"`
	Data    string `json:"data,omitempty"`
	Cols    uint16 `json:"cols,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type terminalSizeQueue struct {
	ch chan remotecommand.TerminalSize
}

func (q *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

type safeWriter struct {
	conn *gorilla.Conn
	mu   sync.Mutex
}

func (w *safeWriter) writeJSON(msg execMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteJSON(msg)
}

type stdoutWriter struct{ ws *safeWriter }

func (w *stdoutWriter) Write(p []byte) (int, error) {
	if err := w.ws.writeJSON(execMessage{Type: "stdout", Data: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

type stderrWriter struct{ ws *safeWriter }

func (w *stderrWriter) Write(p []byte) (int, error) {
	if err := w.ws.writeJSON(execMessage{Type: "stderr", Data: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

var execUpgrader = gorilla.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// detectShell probes the container to find a working shell.
// Runs a quick non-interactive exec to test if the shell binary exists.
func detectShell(clientset kubernetes.Interface, restConfig *restclient.Config, namespace, name, container string) string {
	shells := []string{"/bin/bash", "/bin/sh"}
	for _, sh := range shells {
		req := clientset.CoreV1().RESTClient().Post().
			Resource("pods").Namespace(namespace).Name(name).
			SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{
				Container: container,
				Command:   []string{sh, "-c", "echo ok"},
				Stdout:    true,
				Stderr:    true,
			}, scheme.ParameterCodec)

		exec, err := remotecommand.NewSPDYExecutor(restConfig, "POST", req.URL())
		if err != nil {
			continue
		}

		var stdout, stderr bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdout: &stdout,
			Stderr: &stderr,
		})
		cancel()

		if err == nil && strings.TrimSpace(stdout.String()) == "ok" {
			return sh
		}
	}
	return "/bin/sh" // fallback
}

func (h *handlers) handleExec(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	shell := r.URL.Query().Get("shell")

	log.Printf("Exec request: namespace=%s name=%s container=%s shell=%s", namespace, name, container, shell)

	conn := h.manager.Connector()
	if conn == nil {
		http.Error(w, "cluster not connected", http.StatusServiceUnavailable)
		return
	}

	clientset := conn.Clientset()

	// If no container specified, use the first one
	if container == "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			http.Error(w, "pod not found: "+err.Error(), http.StatusNotFound)
			return
		}
		if len(pod.Spec.Containers) > 0 {
			container = pod.Spec.Containers[0].Name
		}
	}

	// Clone restConfig without timeout
	baseConfig := conn.RestConfig()
	restConfig := *baseConfig
	restConfig.Timeout = 0

	// Detect shell before upgrading WebSocket
	if shell == "" {
		shell = detectShell(clientset, &restConfig, namespace, name, container)
		log.Printf("Detected shell: %s for %s/%s container=%s", shell, namespace, name, container)
	}

	// Build the exec request for the interactive session
	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   []string{shell},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(&restConfig, "POST", req.URL())
	if err != nil {
		http.Error(w, "failed to create executor: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Upgrade to WebSocket
	wsConn, err := execUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer wsConn.Close()

	ws := &safeWriter{conn: wsConn}
	sizeQueue := &terminalSizeQueue{ch: make(chan remotecommand.TerminalSize, 1)}
	stdinReader, stdinWriter := io.Pipe()

	log.Printf("Exec session started: %s/%s container=%s shell=%s", namespace, name, container, shell)

	// Read pump: reads WebSocket messages, feeds stdin pipe and resize queue
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer stdinWriter.Close()
		defer close(sizeQueue.ch)

		for {
			_, raw, err := wsConn.ReadMessage()
			if err != nil {
				return
			}

			var msg execMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}

			switch msg.Type {
			case "stdin":
				if _, err := stdinWriter.Write([]byte(msg.Data)); err != nil {
					return
				}
			case "resize":
				if msg.Cols > 0 && msg.Rows > 0 {
					select {
					case sizeQueue.ch <- remotecommand.TerminalSize{Width: msg.Cols, Height: msg.Rows}:
					default:
						select {
						case <-sizeQueue.ch:
						default:
						}
						sizeQueue.ch <- remotecommand.TerminalSize{Width: msg.Cols, Height: msg.Rows}
					}
				}
			}
		}
	}()

	// Execute — blocks until shell exits
	err = executor.StreamWithContext(r.Context(), remotecommand.StreamOptions{
		Stdin:             stdinReader,
		Stdout:            &stdoutWriter{ws: ws},
		Stderr:            &stderrWriter{ws: ws},
		Tty:               true,
		TerminalSizeQueue: sizeQueue,
	})

	exitCode := 0
	if err != nil {
		log.Printf("Exec stream ended: %v", err)
		if exitErr, ok := err.(interface{ ExitStatus() int }); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			ws.writeJSON(execMessage{Type: "error", Message: err.Error()})
			exitCode = 1
		}
	}

	ws.writeJSON(execMessage{Type: "exit", Code: exitCode})
	<-done
	log.Printf("Exec session closed: %s/%s", namespace, name)
}
