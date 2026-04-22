package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// parseRange maps "24h" | "7d" | "30d" into a time window ending now.
// Defaults to 7d.
func parseRange(q string) (from, to time.Time) {
	to = time.Now()
	switch strings.ToLower(q) {
	case "24h", "1d":
		return to.Add(-24 * time.Hour), to
	case "30d":
		return to.Add(-30 * 24 * time.Hour), to
	case "7d", "":
		return to.Add(-7 * 24 * time.Hour), to
	}
	// Accept numeric hours as a fallback (e.g. "48h" by parsing the digits).
	if strings.HasSuffix(q, "h") {
		if n, err := strconv.Atoi(strings.TrimSuffix(q, "h")); err == nil && n > 0 {
			return to.Add(-time.Duration(n) * time.Hour), to
		}
	}
	if strings.HasSuffix(q, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(q, "d")); err == nil && n > 0 {
			return to.Add(-time.Duration(n) * 24 * time.Hour), to
		}
	}
	return to.Add(-7 * 24 * time.Hour), to
}

// usageSummaryResponse is the shape consumed by the admin "Copilot Usage" tiles.
type usageSummaryResponse struct {
	Range         string  `json:"range"`
	Sessions      int     `json:"sessions"`
	ErrorSessions int     `json:"errorSessions"`
	InputTokens   int     `json:"inputTokens"`
	OutputTokens  int     `json:"outputTokens"`
	CacheRead     int     `json:"cacheReadTokens"`
	CacheCreation int     `json:"cacheCreationTokens"`
	TotalBilled   int     `json:"totalBilledTokens"` // input + output (excludes cache read discount)
	CacheHitPct   float64 `json:"cacheHitPct"`
	AvgRounds     float64 `json:"avgRounds"`
	AvgDurationMs int64   `json:"avgDurationMs"`
	Compacts      int     `json:"compacts"`
	EstimatedUSD  float64 `json:"estimatedUsd"`
	TopTools      []toolSummary `json:"topTools"`
	TopTriggers   map[string]int `json:"topTriggers"`
}

type toolSummary struct {
	Name       string `json:"name"`
	Calls      int    `json:"calls"`
	Errors     int    `json:"errors"`
	TotalBytes int    `json:"bytes"`
}

func (h *handlers) handleCopilotUsageSummary(w http.ResponseWriter, r *http.Request) {
	if h.copilotUsage == nil {
		respondError(w, http.StatusServiceUnavailable, "copilot usage store not initialized (auth disabled)")
		return
	}
	rng := r.URL.Query().Get("range")
	from, to := parseRange(rng)

	records, err := h.copilotUsage.Query(from, to, 0)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := usageSummaryResponse{
		Range:       rng,
		TopTriggers: map[string]int{},
	}
	var durSum int64
	var roundsSum int
	toolAgg := map[string]*toolSummary{}

	for _, rec := range records {
		resp.Sessions++
		if rec.Reason != "done" {
			resp.ErrorSessions++
		}
		resp.InputTokens += rec.Usage.InputTokens
		resp.OutputTokens += rec.Usage.OutputTokens
		resp.CacheRead += rec.Usage.CacheReadTokens
		resp.CacheCreation += rec.Usage.CacheCreationTokens
		resp.Compacts += len(rec.Compacts)
		durSum += rec.DurationMs
		roundsSum += rec.Rounds

		if rec.Trigger != "" {
			resp.TopTriggers[rec.Trigger]++
		}
		for name, t := range rec.Tools {
			agg, ok := toolAgg[name]
			if !ok {
				agg = &toolSummary{Name: name}
				toolAgg[name] = agg
			}
			agg.Calls += t.Calls
			agg.Errors += t.Errors
			agg.TotalBytes += t.Bytes
		}

		if pricing, known := copilot.PricingFor(rec.Provider, rec.Model); known {
			resp.EstimatedUSD += copilot.EstimateUSD(rec.Usage, pricing)
		}
	}

	resp.TotalBilled = resp.InputTokens + resp.OutputTokens
	totalInputWithCache := resp.InputTokens + resp.CacheRead + resp.CacheCreation
	if totalInputWithCache > 0 {
		resp.CacheHitPct = float64(resp.CacheRead) / float64(totalInputWithCache) * 100
	}
	if resp.Sessions > 0 {
		resp.AvgRounds = float64(roundsSum) / float64(resp.Sessions)
		resp.AvgDurationMs = durSum / int64(resp.Sessions)
	}

	// Top 10 tools by call count.
	tools := make([]toolSummary, 0, len(toolAgg))
	for _, t := range toolAgg {
		tools = append(tools, *t)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Calls > tools[j].Calls })
	if len(tools) > 10 {
		tools = tools[:10]
	}
	resp.TopTools = tools

	respondJSON(w, http.StatusOK, resp)
}

type timeseriesBucket struct {
	Time          time.Time `json:"time"`
	Sessions      int       `json:"sessions"`
	InputTokens   int       `json:"inputTokens"`
	OutputTokens  int       `json:"outputTokens"`
	CacheRead     int       `json:"cacheReadTokens"`
	Compacts      int       `json:"compacts"`
	EstimatedUSD  float64   `json:"estimatedUsd"`
}

func (h *handlers) handleCopilotUsageTimeseries(w http.ResponseWriter, r *http.Request) {
	if h.copilotUsage == nil {
		respondError(w, http.StatusServiceUnavailable, "copilot usage store not initialized (auth disabled)")
		return
	}
	rng := r.URL.Query().Get("range")
	from, to := parseRange(rng)

	// Pick a bucket size appropriate for the range.
	bucketSize := time.Hour
	switch strings.ToLower(rng) {
	case "7d":
		bucketSize = 24 * time.Hour
	case "30d":
		bucketSize = 24 * time.Hour
	case "", "24h", "1d":
		bucketSize = time.Hour
	}
	if q := r.URL.Query().Get("bucket"); q != "" {
		switch q {
		case "hour":
			bucketSize = time.Hour
		case "day":
			bucketSize = 24 * time.Hour
		}
	}

	records, err := h.copilotUsage.Query(from, to, 0)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Build buckets across the full range so the chart renders gaps as zero.
	buckets := map[int64]*timeseriesBucket{}
	for t := from.Truncate(bucketSize); !t.After(to); t = t.Add(bucketSize) {
		buckets[t.Unix()] = &timeseriesBucket{Time: t}
	}
	for _, rec := range records {
		key := rec.Timestamp.Truncate(bucketSize).Unix()
		b, ok := buckets[key]
		if !ok {
			b = &timeseriesBucket{Time: rec.Timestamp.Truncate(bucketSize)}
			buckets[key] = b
		}
		b.Sessions++
		b.InputTokens += rec.Usage.InputTokens
		b.OutputTokens += rec.Usage.OutputTokens
		b.CacheRead += rec.Usage.CacheReadTokens
		b.Compacts += len(rec.Compacts)
		if pricing, known := copilot.PricingFor(rec.Provider, rec.Model); known {
			b.EstimatedUSD += copilot.EstimateUSD(rec.Usage, pricing)
		}
	}

	out := make([]timeseriesBucket, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	respondJSON(w, http.StatusOK, out)
}

func (h *handlers) handleCopilotUsageSessions(w http.ResponseWriter, r *http.Request) {
	if h.copilotUsage == nil {
		respondError(w, http.StatusServiceUnavailable, "copilot usage store not initialized (auth disabled)")
		return
	}
	rng := r.URL.Query().Get("range")
	from, to := parseRange(rng)
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	records, err := h.copilotUsage.Query(from, to, 0)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(records) > limit {
		records = records[:limit]
	}

	// Attach estimated cost per record for the UI, without mutating the
	// stored record. Wrap into a response shape.
	type enrichedSession struct {
		copilot.SessionRecord
		EstimatedUSD float64 `json:"estimatedUsd"`
	}
	enriched := make([]enrichedSession, 0, len(records))
	for _, rec := range records {
		item := enrichedSession{SessionRecord: rec}
		if pricing, known := copilot.PricingFor(rec.Provider, rec.Model); known {
			item.EstimatedUSD = copilot.EstimateUSD(rec.Usage, pricing)
		}
		enriched = append(enriched, item)
	}
	respondJSON(w, http.StatusOK, enriched)
}
