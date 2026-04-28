package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/kubebolt/kubebolt/apps/api/internal/cluster"
)

type fileEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // dir, file, link
	Size        string `json:"size"`
	Modified    string `json:"modified,omitempty"`
	Permissions string `json:"permissions,omitempty"`
}

// execCommand runs a non-interactive command in a pod container and returns stdout/stderr.
func (h *handlers) execCommand(namespace, name, container string, command []string) (string, string, error) {
	conn := h.manager.Connector()
	if conn == nil {
		return "", "", fmt.Errorf("cluster not connected")
	}

	baseConfig := conn.RestConfig()
	restConfig := *baseConfig
	restConfig.Timeout = 0

	clientset := conn.Clientset()

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	transport, upgrader, err := cluster.SPDYTransportsFor(&restConfig)
	if err != nil {
		return "", "", fmt.Errorf("building SPDY transports: %w", err)
	}
	executor, err := remotecommand.NewSPDYExecutorForTransports(transport, upgrader, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("creating executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	return stdout.String(), stderr.String(), err
}

func sanitizePath(p string) string {
	// Clean the path and ensure it's absolute
	cleaned := path.Clean("/" + p)
	// Prevent any traversal
	if strings.Contains(cleaned, "..") {
		return "/"
	}
	return cleaned
}

func (h *handlers) handleListFiles(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	dirPath := sanitizePath(r.URL.Query().Get("path"))

	if namespace == "_" {
		namespace = ""
	}

	// Try ls variants, then fall back to find for minimal containers
	stdout, stderr, err := h.execCommand(namespace, name, container,
		[]string{"ls", "-la", "--time-style=long-iso", dirPath})
	if err != nil || strings.Contains(stderr, "unrecognized option") {
		stdout, stderr, err = h.execCommand(namespace, name, container,
			[]string{"ls", "-la", dirPath})
	}
	if err != nil {
		// Fallback: use find for distroless/minimal containers without ls
		stdout, stderr, err = h.execCommand(namespace, name, container,
			[]string{"find", dirPath, "-maxdepth", "1", "-printf", "%y %s %f\n"})
		if err != nil {
			errMsg := stderr
			if errMsg == "" {
				errMsg = err.Error()
			}
			if strings.Contains(errMsg, "executable file not found") || strings.Contains(errMsg, "not found in $PATH") {
				respondError(w, http.StatusBadRequest, "This container does not have ls or find — file browsing is not available for minimal/distroless images")
				return
			}
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("file listing failed: %s", errMsg))
			return
		}
		// Parse find output
		entries := parseFindOutput(stdout, dirPath)
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"path":  dirPath,
			"items": entries,
		})
		return
	}

	entries := parseLsOutput(stdout, dirPath)
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"path":  dirPath,
		"items": entries,
	})
}

func parseLsOutput(output, dirPath string) []fileEntry {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var entries []fileEntry

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}

		perms := fields[0]
		size := fields[4]
		name := fields[len(fields)-1]

		// Skip . and .. and hidden symlink targets (..2026_xxx style)
		if name == "." || name == ".." || strings.HasPrefix(name, "..") {
			continue
		}

		// Handle symlinks: name -> target
		if strings.Contains(line, " -> ") {
			parts := strings.SplitN(line, " -> ", 2)
			nameFields := strings.Fields(parts[0])
			name = nameFields[len(nameFields)-1]
		}

		// Determine modified time (try to find date-like fields)
		modified := ""
		for i := 5; i < len(fields)-1; i++ {
			if len(fields[i]) >= 4 && (fields[i][0] >= '0' && fields[i][0] <= '9') {
				// Collect date/time fields
				remaining := strings.Join(fields[i:len(fields)-1], " ")
				if idx := strings.Index(remaining, name); idx > 0 {
					remaining = strings.TrimSpace(remaining[:idx])
				}
				modified = remaining
				break
			}
		}

		entryType := "file"
		if perms[0] == 'd' {
			entryType = "dir"
		} else if perms[0] == 'l' {
			entryType = "link"
		}

		entries = append(entries, fileEntry{
			Name:        name,
			Type:        entryType,
			Size:        size,
			Modified:    modified,
			Permissions: perms,
		})
	}

	return entries
}

func parseFindOutput(output, dirPath string) []fileEntry {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var entries []fileEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		typeChar := fields[0]
		size := fields[1]
		name := strings.Join(fields[2:], " ")
		if name == "." || name == ".." || name == filepath.Base(dirPath) {
			continue
		}
		entryType := "file"
		if typeChar == "d" {
			entryType = "dir"
		} else if typeChar == "l" {
			entryType = "link"
		}
		entries = append(entries, fileEntry{
			Name: name,
			Type: entryType,
			Size: size,
		})
	}
	return entries
}

func (h *handlers) handleFileContent(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	filePath := sanitizePath(r.URL.Query().Get("path"))

	if namespace == "_" {
		namespace = ""
	}

	// Check file size first (1MB limit)
	sizeOut, _, _ := h.execCommand(namespace, name, container,
		[]string{"stat", "-c", "%s", filePath})
	sizeStr := strings.TrimSpace(sizeOut)
	if len(sizeStr) > 0 {
		size := 0
		fmt.Sscanf(sizeStr, "%d", &size)
		if size > 1024*1024 {
			respondError(w, http.StatusBadRequest, fmt.Sprintf("file too large (%s bytes, max 1MB)", sizeStr))
			return
		}
	}

	stdout, stderr, err := h.execCommand(namespace, name, container,
		[]string{"cat", filePath})
	if err != nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("cannot read file: %s %s", stderr, err))
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(stdout))
}

func (h *handlers) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	filePath := sanitizePath(r.URL.Query().Get("path"))

	if namespace == "_" {
		namespace = ""
	}

	stdout, stderr, err := h.execCommand(namespace, name, container,
		[]string{"cat", filePath})
	if err != nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("cannot read file: %s %s", stderr, err))
		return
	}

	fileName := filepath.Base(filePath)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(stdout))
}
