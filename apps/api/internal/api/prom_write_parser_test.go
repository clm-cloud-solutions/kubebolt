package api

import (
	"errors"
	"testing"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

// buildTimeSeriesBytes assembles the inner protobuf wire payload of a
// single TimeSeries with `labelCount` labels and `sampleCount` samples.
// We craft the bytes by hand to keep the test free of the
// prometheus/prompb dependency the parser itself avoids.
//
// Sample (field 2) is encoded as a length-delimited bytes value — for
// the parser's purposes the only thing that matters is the field
// number + wire type combo, not the content. We emit a 2-byte
// placeholder for each sample.
func buildTimeSeriesBytes(labelCount, sampleCount int) []byte {
	var buf []byte
	// labels: field 1, bytes-typed. Empty payload is fine — the parser
	// skips them.
	for i := 0; i < labelCount; i++ {
		buf = protowire.AppendTag(buf, 1, protowire.BytesType)
		buf = protowire.AppendBytes(buf, []byte{}) // empty Label
	}
	// samples: field 2, bytes-typed. Each sample is a Sample message
	// — we emit a placeholder body so the wire format is valid.
	for i := 0; i < sampleCount; i++ {
		buf = protowire.AppendTag(buf, 2, protowire.BytesType)
		buf = protowire.AppendBytes(buf, []byte{0x09, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	}
	return buf
}

// buildWriteRequest emits a top-level WriteRequest with N TimeSeries.
// Each carries the supplied label/sample counts.
func buildWriteRequest(timeseries []struct{ Labels, Samples int }) []byte {
	var buf []byte
	for _, ts := range timeseries {
		inner := buildTimeSeriesBytes(ts.Labels, ts.Samples)
		buf = protowire.AppendTag(buf, 1, protowire.BytesType) // field 1: timeseries
		buf = protowire.AppendBytes(buf, inner)
	}
	return buf
}

func TestCountSamplesInWriteRequest_Empty(t *testing.T) {
	n, err := countSamplesInWriteRequest(nil)
	if err != nil || n != 0 {
		t.Fatalf("empty body: expected 0, nil; got %d, %v", n, err)
	}
}

func TestCountSamplesInWriteRequest_SingleSeriesSingleSample(t *testing.T) {
	body := buildWriteRequest([]struct{ Labels, Samples int }{
		{Labels: 2, Samples: 1},
	})
	n, err := countSamplesInWriteRequest(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 sample, got %d", n)
	}
}

func TestCountSamplesInWriteRequest_MultipleSeries(t *testing.T) {
	body := buildWriteRequest([]struct{ Labels, Samples int }{
		{Labels: 3, Samples: 5},
		{Labels: 2, Samples: 10},
		{Labels: 1, Samples: 0}, // zero samples on a series is legal
		{Labels: 4, Samples: 7},
	})
	n, err := countSamplesInWriteRequest(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := 5 + 10 + 0 + 7
	if n != want {
		t.Errorf("expected %d samples, got %d", want, n)
	}
}

func TestCountSamplesInWriteRequest_UnknownTopLevelField(t *testing.T) {
	// Future Prom versions may add field 2 / 3 / etc. at the top
	// level. The parser must skip those gracefully, not error.
	body := buildWriteRequest([]struct{ Labels, Samples int }{
		{Labels: 1, Samples: 3},
	})
	// Append an unknown varint field (number 99, wire type 0).
	body = protowire.AppendTag(body, 99, protowire.VarintType)
	body = protowire.AppendVarint(body, 42)
	n, err := countSamplesInWriteRequest(body)
	if err != nil {
		t.Fatalf("unexpected error skipping unknown top-level field: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 samples (unknown field skipped), got %d", n)
	}
}

func TestCountSamplesInWriteRequest_MalformedRejects(t *testing.T) {
	// Truncated tag bytes — protowire returns a negative length.
	body := []byte{0xFF} // single high-bit byte means varint isn't complete
	_, err := countSamplesInWriteRequest(body)
	if err == nil {
		t.Fatalf("malformed body should error, got nil")
	}
}

func TestCountWriteRequestSamples_SnappyRoundTrip(t *testing.T) {
	body := buildWriteRequest([]struct{ Labels, Samples int }{
		{Labels: 2, Samples: 42},
	})
	encoded := snappy.Encode(nil, body)
	n, err := countWriteRequestSamples(encoded)
	if err != nil {
		t.Fatalf("snappy round-trip: %v", err)
	}
	if n != 42 {
		t.Errorf("expected 42 samples through snappy, got %d", n)
	}
}

func TestCountWriteRequestSamples_DecodedTooLargeRejects(t *testing.T) {
	// Construct a snappy stream whose decoded length header exceeds
	// promWriteMaxDecodedBytes. Easiest: encode a payload at the
	// limit + 1, the header will reflect it.
	big := make([]byte, promWriteMaxDecodedBytes+1)
	encoded := snappy.Encode(nil, big)
	_, err := countWriteRequestSamples(encoded)
	if err == nil {
		t.Fatalf("oversized decoded payload should error")
	}
	if !errors.Is(err, ErrPromWriteTooLarge) {
		t.Errorf("expected ErrPromWriteTooLarge, got %v", err)
	}
}

func TestCountWriteRequestSamples_BadSnappyHeader(t *testing.T) {
	_, err := countWriteRequestSamples([]byte{0x00, 0x01})
	if err == nil {
		t.Fatalf("bad snappy stream should error")
	}
}
