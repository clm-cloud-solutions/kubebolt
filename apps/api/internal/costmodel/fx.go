package costmodel

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FX converts between currencies. OpenCost prices come in its own currency
// (USD by default) and an org may be billed in euros, so a plan's money needs
// a rate (decision P9). Rate reports false when there is none: the amount then
// stays in OpenCost's currency, unconverted, and says so.
type FX interface {
	Rate(ctx context.Context, from, to string) (float64, bool)
}

// ECBDailyURL is the European Central Bank's daily reference rates: euros
// against ~30 currencies, published on working days around 16:00 CET.
const ECBDailyURL = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"

// ECB is an FX on the ECB's daily reference rates, fetched at most once per
// TTL (24 h, P9). When a refresh fails it keeps answering with the last rates
// it got, for up to MaxStale, so a short outage of the ECB does not take the
// money out of every plan. With nothing fetched yet, or rates older than
// MaxStale, it has no rate.
type ECB struct {
	URL      string
	Client   *http.Client
	TTL      time.Duration
	MaxStale time.Duration
	Now      func() time.Time

	mu        sync.Mutex
	perEUR    map[string]float64 // currency (lower case) → units per euro; eur = 1
	fetchedAt time.Time
	triedAt   time.Time
}

// NewECB returns the ECB source with the production settings.
func NewECB() *ECB {
	return &ECB{
		URL:      ECBDailyURL,
		Client:   &http.Client{Timeout: 5 * time.Second},
		TTL:      24 * time.Hour,
		MaxStale: 7 * 24 * time.Hour,
		Now:      time.Now,
	}
}

// retryAfterFailure spaces out attempts while the ECB is failing, so every
// plan built during an outage does not wait on a timeout.
const retryAfterFailure = 15 * time.Minute

func (e *ECB) Rate(ctx context.Context, from, to string) (float64, bool) {
	from, to = strings.ToLower(from), strings.ToLower(to)
	if from == "" || to == "" {
		return 0, false
	}
	if from == to {
		return 1, true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.Now()
	if now.Sub(e.fetchedAt) >= e.TTL && now.Sub(e.triedAt) >= retryAfterFailure {
		e.triedAt = now
		// The caller's cancellation is not the ECB failing: a cancelled
		// request must not hold every plan off the rate for retryAfterFailure.
		if rates, err := e.fetch(context.WithoutCancel(ctx)); err == nil {
			e.perEUR, e.fetchedAt = rates, now
		}
	}
	if e.perEUR == nil || now.Sub(e.fetchedAt) > e.MaxStale {
		return 0, false
	}
	f, okF := e.perEUR[from]
	t, okT := e.perEUR[to]
	if !okF || !okT || f <= 0 {
		return 0, false
	}
	return t / f, true
}

type ecbEnvelope struct {
	Cubes []struct {
		Currency string `xml:"currency,attr"`
		Rate     string `xml:"rate,attr"`
	} `xml:"Cube>Cube>Cube"`
}

func (e *ECB) fetch(ctx context.Context) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ecb: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var env ecbEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("ecb: %w", err)
	}
	rates := map[string]float64{"eur": 1}
	for _, c := range env.Cubes {
		v, err := strconv.ParseFloat(c.Rate, 64)
		if err != nil || v <= 0 || c.Currency == "" {
			continue
		}
		rates[strings.ToLower(c.Currency)] = v
	}
	if len(rates) == 1 {
		return nil, fmt.Errorf("ecb: no rates in the document")
	}
	return rates, nil
}
