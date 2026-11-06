package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"

	"github.com/kubebolt/kubebolt/apps/api/internal/opsmetrics"
)

// VMWriter writes samples to VictoriaMetrics via its Prometheus plain-text
// import endpoint. This is the Sprint 0 choice: simpler than remote_write
// (no snappy/protobuf), still a production-grade VM endpoint. Sprint 2+
// switches to remote_write for efficiency.
type VMWriter struct {
	endpoint string
	client   *http.Client
}

func NewVMWriter(endpoint string) *VMWriter {
	return &VMWriter{
		endpoint: strings.TrimRight(endpoint, "/"),
		client:   opsmetrics.VMClient("agent_ingest", 10*time.Second),
	}
}

func (w *VMWriter) Write(ctx context.Context, samples []*agentv2.Sample) error {
	if len(samples) == 0 {
		return nil
	}
	var buf bytes.Buffer
	var keys []string
	for _, s := range samples {
		if s.GetMetricName() == "" {
			continue
		}
		buf.WriteString(s.GetMetricName())
		if labels := s.GetLabels(); len(labels) > 0 {
			// Labels are written sorted by name. A Go map ranges in a
			// different order on every call, and VictoriaMetrics keys its
			// storage/tsid cache on the series text exactly as received
			// (unless it runs with -sortLabels): every order was a key of
			// its own. The cache filled with permutations of the same
			// series and most inserts missed it and fell back to an
			// indexdb lookup (one install measured 81% slow inserts and
			// 2.2M cache entries for ~40k active series).
			keys = keys[:0]
			for k := range labels {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			buf.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					buf.WriteByte(',')
				}
				buf.WriteString(k)
				buf.WriteString(`="`)
				buf.WriteString(escapeLabelValue(labels[k]))
				buf.WriteByte('"')
			}
			buf.WriteByte('}')
		}
		buf.WriteByte(' ')
		buf.WriteString(strconv.FormatFloat(s.GetValue(), 'g', -1, 64))
		buf.WriteByte(' ')
		var ts int64
		if pts := s.GetTimestamp(); pts != nil {
			ts = pts.AsTime().UnixMilli()
		} else {
			ts = time.Now().UnixMilli()
		}
		buf.WriteString(strconv.FormatInt(ts, 10))
		buf.WriteByte('\n')
	}
	if buf.Len() == 0 {
		return nil
	}

	url := w.endpoint + "/api/v1/import/prometheus"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("vm write: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("vm write status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func escapeLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}
