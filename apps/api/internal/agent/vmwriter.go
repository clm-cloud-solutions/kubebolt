package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v1"
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
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (w *VMWriter) Write(ctx context.Context, samples []*agentv1.Sample) error {
	if len(samples) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, s := range samples {
		if s.GetMetricName() == "" {
			continue
		}
		buf.WriteString(s.GetMetricName())
		if labels := s.GetLabels(); len(labels) > 0 {
			buf.WriteByte('{')
			first := true
			for k, v := range labels {
				if !first {
					buf.WriteByte(',')
				}
				buf.WriteString(k)
				buf.WriteString(`="`)
				buf.WriteString(escapeLabelValue(v))
				buf.WriteByte('"')
				first = false
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
