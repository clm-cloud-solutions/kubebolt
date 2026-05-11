package api

import (
	"errors"
	"fmt"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

// countWriteRequestSamples decodes a Prom remote_write payload just
// far enough to count the total number of Samples it carries. The
// count is the natural unit for per-tenant rate limiting and quota
// accounting in the SaaS edition: every sample is a billable event.
//
// We deliberately avoid pulling in `github.com/prometheus/prometheus/prompb`
// for this — the wire format we need to inspect is trivial and the
// full prometheus prompb dependency would drag a lot of unrelated
// types into a backend that only needs to count.
//
// Algorithm (one snappy decode + one linear protowire scan):
//
//	WriteRequest {
//	  repeated TimeSeries timeseries = 1;   // wire type 2 (length-delimited)
//	}
//	TimeSeries {
//	  repeated Label  labels = 1;
//	  repeated Sample samples = 2;          // wire type 2
//	}
//
// Walk the top-level WriteRequest, peel each field-1 (TimeSeries),
// then walk inside it and count each field-2 (Sample) occurrence.
// Other fields (labels, exemplars on newer Prom versions, etc.)
// are skipped via protowire.ConsumeFieldValue which knows how to
// move past any wire type. The function tolerates unknown extra
// fields — Prometheus may add new top-level fields to WriteRequest
// in future revisions and our counter must keep working.
//
// Returns the decoded payload alongside the sample count so the
// downstream Day 4 validators (readTenantIDFromFirstSeries,
// injectTenantID) can reuse the snappy decode work. The caller is
// responsible for forwarding the ORIGINAL snappy-compressed body to
// VM in the happy path; the decoded slice is only for inspection.
// On the auto-stamp fallback path the caller re-encodes the (modified)
// decoded bytes into a fresh snappy frame.
//
// Errors are surfaced as classified types so the handler can map them
// to HTTP 400 (the client sent invalid wire format) rather than 500.
func countWriteRequestSamples(snappyBody []byte) (count int, decoded []byte, err error) {
	// Defensive: snappy's Decode allocates the decoded slice, so a
	// pathological "claims 10 GiB" header from a buggy/malicious
	// client would OOM us. snappy.DecodedLen returns the announced
	// uncompressed size from the header — clamp before allocating.
	decodedLen, err := snappy.DecodedLen(snappyBody)
	if err != nil {
		return 0, nil, fmt.Errorf("snappy header: %w", err)
	}
	if decodedLen > promWriteMaxDecodedBytes {
		return 0, nil, fmt.Errorf("decoded size %d exceeds cap %d: %w", decodedLen, promWriteMaxDecodedBytes, ErrPromWriteTooLarge)
	}

	decoded, err = snappy.Decode(nil, snappyBody)
	if err != nil {
		return 0, nil, fmt.Errorf("snappy decode: %w", err)
	}
	count, err = countSamplesInWriteRequest(decoded)
	if err != nil {
		return 0, nil, err
	}
	return count, decoded, nil
}

// promWriteMaxDecodedBytes caps the SIZE OF THE DECOMPRESSED payload
// (i.e. after snappy expands the wire bytes). The on-wire cap
// `promWriteMaxBodyBytes` limits the compressed input; this one
// limits how much memory the decompression step is allowed to
// allocate before we even start the proto scan. The 4× factor
// matches typical Prom remote_write compression ratios — a 16 MiB
// snappy-compressed batch expands to ~50-60 MiB of raw protobuf.
const promWriteMaxDecodedBytes = 4 * promWriteMaxBodyBytes // 64 MiB

// ErrPromWriteTooLarge is the sentinel for body / decoded size
// overflow. The handler maps it to HTTP 413.
var ErrPromWriteTooLarge = errors.New("remote_write payload too large")

// TenantIDLabelName is the label key that carries the tenant identity
// on every series shipped through the Prom remote_write path. Agents
// (Phase 3 Day 4.2) stamp it via vmagent external_labels OR via the
// agent process's own label injection; the receiver validates it
// against the bearer token's tenant. The convention is "tenant_id"
// (snake_case, no namespace prefix) because Prometheus convention
// favors lowercase ascii for label names that operators may write
// in queries.
const TenantIDLabelName = "tenant_id"

// readTenantIDFromFirstSeries scans the first TimeSeries of the
// decoded WriteRequest looking for the tenant_id label. Returns the
// asserted tenant_id and a `found` flag.
//
// We deliberately only inspect the first TimeSeries: in a single
// remote_write request, all series share the same external_labels
// stamped by the producer (vmagent / external Prom). So one is
// representative of the whole batch — saves N iterations per request.
//
// The receiver's anti-spoofing logic uses this to compare against
// the bearer token's tenant. The auto-stamp fallback uses the `found
// == false` signal as the trigger to inject.
//
// Edge cases handled:
//   - Empty WriteRequest (no TimeSeries) → (empty, false)
//   - First TimeSeries has no labels at all → (empty, false)
//   - tenant_id label appears multiple times in one TimeSeries → returns
//     the FIRST occurrence (subsequent values ignored; the validator
//     above takes care of mismatch detection)
//   - tenant_id present in a non-first TimeSeries but not the first →
//     reads as absent. This is OK because vmagent/Prom external_labels
//     uniformity means heterogeneous tenant labeling across a single
//     request is a configuration error we'd want to reject anyway.
func readTenantIDFromFirstSeries(decoded []byte) (tenantID string, found bool) {
	const (
		fieldTimeSeries = 1 // WriteRequest.timeseries
		fieldLabels     = 1 // TimeSeries.labels
		fieldLabelName  = 1 // Label.name
		fieldLabelValue = 2 // Label.value
	)
	rem := decoded
	for len(rem) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			return "", false
		}
		rem = rem[tagLen:]
		if num == fieldTimeSeries && typ == protowire.BytesType {
			tsBytes, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				return "", false
			}
			// Walk this TimeSeries' labels — return on first tenant_id hit.
			inner := tsBytes
			for len(inner) > 0 {
				innerNum, innerTyp, innerTagLen := protowire.ConsumeTag(inner)
				if innerTagLen < 0 {
					return "", false
				}
				inner = inner[innerTagLen:]
				if innerNum == fieldLabels && innerTyp == protowire.BytesType {
					labelBytes, m := protowire.ConsumeBytes(inner)
					if m < 0 {
						return "", false
					}
					inner = inner[m:]
					name, value, ok := parseLabelNameValue(labelBytes)
					if ok && name == TenantIDLabelName {
						return value, true
					}
					continue
				}
				// Skip non-label fields (samples, exemplars, etc.)
				skip := protowire.ConsumeFieldValue(innerNum, innerTyp, inner)
				if skip < 0 {
					return "", false
				}
				inner = inner[skip:]
			}
			// First TimeSeries walked, tenant_id not found.
			return "", false
		}
		// Skip non-TimeSeries top-level fields.
		skip := protowire.ConsumeFieldValue(num, typ, rem)
		if skip < 0 {
			return "", false
		}
		rem = rem[skip:]
	}
	return "", false
}

// parseLabelNameValue extracts Label.name + Label.value from a raw
// Label record's bytes. Returns the name/value pair and a found flag
// that's false on parse error. Both fields are guaranteed bytes-typed
// (strings) per the Prometheus prompb proto.
func parseLabelNameValue(labelBytes []byte) (name, value string, ok bool) {
	const (
		fieldName  = 1
		fieldValue = 2
	)
	rem := labelBytes
	var gotName, gotValue bool
	for len(rem) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			return "", "", false
		}
		rem = rem[tagLen:]
		if typ == protowire.BytesType {
			s, n := protowire.ConsumeString(rem)
			if n < 0 {
				return "", "", false
			}
			rem = rem[n:]
			switch num {
			case fieldName:
				name = s
				gotName = true
			case fieldValue:
				value = s
				gotValue = true
			}
			continue
		}
		skip := protowire.ConsumeFieldValue(num, typ, rem)
		if skip < 0 {
			return "", "", false
		}
		rem = rem[skip:]
	}
	return name, value, gotName && gotValue
}

// countSamplesInWriteRequest is the inner protobuf-wire scanner.
// Exported as a separate function so tests can drive it with raw
// protobuf bytes (no snappy round-trip needed).
func countSamplesInWriteRequest(decoded []byte) (int, error) {
	const (
		fieldTimeSeries = 1 // WriteRequest.timeseries
		fieldSamples    = 2 // TimeSeries.samples
	)
	count := 0
	rem := decoded
	for len(rem) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			return 0, fmt.Errorf("protowire tag: %w", protowire.ParseError(tagLen))
		}
		rem = rem[tagLen:]
		if num == fieldTimeSeries && typ == protowire.BytesType {
			tsBytes, n := protowire.ConsumeBytes(rem)
			if n < 0 {
				return 0, fmt.Errorf("protowire timeseries bytes: %w", protowire.ParseError(n))
			}
			rem = rem[n:]
			// Inner scan: count Sample entries.
			inner := tsBytes
			for len(inner) > 0 {
				innerNum, innerTyp, innerTagLen := protowire.ConsumeTag(inner)
				if innerTagLen < 0 {
					return 0, fmt.Errorf("protowire ts inner tag: %w", protowire.ParseError(innerTagLen))
				}
				inner = inner[innerTagLen:]
				if innerNum == fieldSamples && innerTyp == protowire.BytesType {
					count++
				}
				skipLen := protowire.ConsumeFieldValue(innerNum, innerTyp, inner)
				if skipLen < 0 {
					return 0, fmt.Errorf("protowire ts inner skip: %w", protowire.ParseError(skipLen))
				}
				inner = inner[skipLen:]
			}
			continue
		}
		// Skip unknown / non-timeseries fields at the top level.
		skipLen := protowire.ConsumeFieldValue(num, typ, rem)
		if skipLen < 0 {
			return 0, fmt.Errorf("protowire top-level skip: %w", protowire.ParseError(skipLen))
		}
		rem = rem[skipLen:]
	}
	return count, nil
}
