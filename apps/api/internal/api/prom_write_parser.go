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
// The decoded payload is returned alongside the sample count: the
// caller already needed the raw bytes (for forwarding to VM), so we
// avoid a second snappy round-trip. The original snappy-compressed
// body is preserved in the calling code for the actual forward; this
// helper exists purely for the count.
//
// Errors are surfaced as classified types so the handler can map them
// to HTTP 400 (the client sent invalid wire format) rather than 500.
func countWriteRequestSamples(snappyBody []byte) (count int, err error) {
	// Defensive: snappy's Decode allocates the decoded slice, so a
	// pathological "claims 10 GiB" header from a buggy/malicious
	// client would OOM us. snappy.DecodedLen returns the announced
	// uncompressed size from the header — clamp before allocating.
	decodedLen, err := snappy.DecodedLen(snappyBody)
	if err != nil {
		return 0, fmt.Errorf("snappy header: %w", err)
	}
	if decodedLen > promWriteMaxDecodedBytes {
		return 0, fmt.Errorf("decoded size %d exceeds cap %d: %w", decodedLen, promWriteMaxDecodedBytes, ErrPromWriteTooLarge)
	}

	decoded, err := snappy.Decode(nil, snappyBody)
	if err != nil {
		return 0, fmt.Errorf("snappy decode: %w", err)
	}
	return countSamplesInWriteRequest(decoded)
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
