package promread

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// QueryRangeResponse mirrors the subset of the Prometheus HTTP API
// /api/v1/query_range payload that this package consumes. Extra
// fields are ignored by json.Unmarshal.
type QueryRangeResponse struct {
	Status    string         `json:"status"`
	Data      QueryRangeData `json:"data"`
	ErrorType string         `json:"errorType,omitempty"`
	Error     string         `json:"error,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
}

type QueryRangeData struct {
	ResultType string             `json:"resultType"`
	Result     []QueryRangeResult `json:"result"`
}

type QueryRangeResult struct {
	Metric map[string]string `json:"metric"`
	// Values is [[timestamp_seconds, "value_string"], ...]. Prom
	// uses float for the timestamp (allows sub-second resolution)
	// and string for the value (preserves NaN / +Inf / -Inf shapes).
	Values [][]interface{} `json:"values"`
}

// Client is a lean Prom-API client scoped to /api/v1/query_range.
// Other Prom endpoints aren't needed for Mode C's purpose; they can
// be added when a use case appears.
type Client struct {
	baseURL string
	http    *http.Client
	auth    Provider
}

// NewClient constructs a Client. baseURL is the customer's Prom
// endpoint without any trailing path (e.g. "https://amp.example/").
// Trailing slashes are tolerated and trimmed.
func NewClient(baseURL string, auth Provider) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
		auth:    auth,
	}
}

// QueryRange fires GET /api/v1/query_range and returns the decoded
// response. Non-2xx HTTP status, non-"success" Prom status, and
// network errors all surface as errors. The caller decides whether
// to retry / fail-open.
func (c *Client) QueryRange(
	ctx context.Context,
	query string,
	start, end time.Time,
	step time.Duration,
) (*QueryRangeResponse, error) {
	u, err := url.Parse(c.baseURL + "/api/v1/query_range")
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", strconv.Itoa(int(step.Seconds())))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if err := c.auth.Apply(req); err != nil {
		return nil, fmt.Errorf("apply auth: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("prometheus http %d: %s", resp.StatusCode, truncate(body, 200))
	}

	var out QueryRangeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s: %s", out.ErrorType, out.Error)
	}
	return &out, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
