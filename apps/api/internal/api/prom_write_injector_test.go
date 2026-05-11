package api

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// buildLabel emits a single Label record body (Label.name + Label.value).
// The caller wraps it in a TimeSeries.labels field (field 1) when
// inserting into a TimeSeries body.
func buildLabel(name, value string) []byte {
	var buf []byte
	buf = protowire.AppendTag(buf, 1, protowire.BytesType) // Label.name
	buf = protowire.AppendString(buf, name)
	buf = protowire.AppendTag(buf, 2, protowire.BytesType) // Label.value
	buf = protowire.AppendString(buf, value)
	return buf
}

// buildTimeSeriesWithLabels emits a full TimeSeries body with the
// supplied labels (each pair becomes a Label record) and `sampleCount`
// placeholder samples. Used by the injector tests to construct
// inputs that already have labels — so we can verify the injector
// PREPENDS its label without breaking the existing ones.
func buildTimeSeriesWithLabels(labels [][2]string, sampleCount int) []byte {
	var buf []byte
	for _, kv := range labels {
		labelBody := buildLabel(kv[0], kv[1])
		buf = protowire.AppendTag(buf, 1, protowire.BytesType) // TS.labels
		buf = protowire.AppendBytes(buf, labelBody)
	}
	for i := 0; i < sampleCount; i++ {
		buf = protowire.AppendTag(buf, 2, protowire.BytesType) // TS.samples
		buf = protowire.AppendBytes(buf, []byte{0x09, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	}
	return buf
}

// buildWriteRequestRich emits a WriteRequest with TimeSeries each
// carrying actual labels. Useful for round-trip tests where we want
// to verify the injector adds tenant_id without dropping other labels.
func buildWriteRequestRich(series []struct {
	Labels  [][2]string
	Samples int
}) []byte {
	var buf []byte
	for _, ts := range series {
		body := buildTimeSeriesWithLabels(ts.Labels, ts.Samples)
		buf = protowire.AppendTag(buf, 1, protowire.BytesType) // WR.timeseries
		buf = protowire.AppendBytes(buf, body)
	}
	return buf
}

func TestInjectTenantID_EmptyRequest(t *testing.T) {
	out, err := injectTenantID(nil, "tenant-A")
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("empty input should produce empty output, got %d bytes", len(out))
	}
}

func TestInjectTenantID_AddsLabelToSingleTimeSeries(t *testing.T) {
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "up"}, {"job", "node"}}, Samples: 3},
	})
	stamped, err := injectTenantID(body, "tenant-X")
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	// Verify via the read helper that tenant_id is now visible.
	got, found := readTenantIDFromFirstSeries(stamped)
	if !found {
		t.Fatalf("after inject, tenant_id should be present")
	}
	if got != "tenant-X" {
		t.Errorf("expected tenant_id=tenant-X, got %q", got)
	}
	// Sample count must still be 3 — injector must not touch samples.
	n, err := countSamplesInWriteRequest(stamped)
	if err != nil {
		t.Fatalf("count after inject: %v", err)
	}
	if n != 3 {
		t.Errorf("inject should preserve sample count, got %d expected 3", n)
	}
}

func TestInjectTenantID_AddsLabelToEveryTimeSeries(t *testing.T) {
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "a"}}, Samples: 1},
		{Labels: [][2]string{{"__name__", "b"}}, Samples: 2},
		{Labels: [][2]string{{"__name__", "c"}}, Samples: 5},
	})
	stamped, err := injectTenantID(body, "tenant-multi")
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	// Count timeseries via parser to ensure we still have 3.
	tsCount := 0
	rem := stamped
	for len(rem) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			t.Fatalf("malformed output at offset %d", len(stamped)-len(rem))
		}
		rem = rem[tagLen:]
		if num == 1 && typ == protowire.BytesType {
			tsCount++
		}
		skip := protowire.ConsumeFieldValue(num, typ, rem)
		if skip < 0 {
			t.Fatalf("malformed value at offset %d", len(stamped)-len(rem))
		}
		rem = rem[skip:]
	}
	if tsCount != 3 {
		t.Errorf("expected 3 TimeSeries preserved, got %d", tsCount)
	}
	// Verify total sample count survived round-trip.
	n, err := countSamplesInWriteRequest(stamped)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 8 {
		t.Errorf("expected 8 samples, got %d", n)
	}
}

func TestInjectTenantID_PassThroughOfNonTimeSeriesFields(t *testing.T) {
	// Simulate a future-Prom WriteRequest with a non-TimeSeries field
	// (e.g. field 3 metadata in newer Prom versions). The injector must
	// pass it through unchanged.
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "x"}}, Samples: 1},
	})
	// Append an unknown varint field at the top level.
	body = protowire.AppendTag(body, 99, protowire.VarintType)
	body = protowire.AppendVarint(body, 12345)

	stamped, err := injectTenantID(body, "tenant-passthrough")
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	// Confirm the unknown field survived. Walk the output, look for field 99.
	foundUnknown := false
	rem := stamped
	for len(rem) > 0 {
		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			t.Fatalf("malformed")
		}
		rem = rem[tagLen:]
		if num == 99 {
			foundUnknown = true
		}
		skip := protowire.ConsumeFieldValue(num, typ, rem)
		if skip < 0 {
			t.Fatalf("malformed value")
		}
		rem = rem[skip:]
	}
	if !foundUnknown {
		t.Errorf("unknown top-level field 99 was lost during inject")
	}
}

func TestInjectTenantID_RoundTripIdempotent(t *testing.T) {
	// Stamping a request that already has tenant_id should still
	// produce parseable output — the second tenant_id label simply
	// becomes a duplicate label, which Prom/VM treat as the FIRST
	// occurrence winning (per the label-set semantics). The injector
	// is meant for the "absent" path; for ergonomics we still want
	// it to not break when called on an already-stamped payload.
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{TenantIDLabelName, "first-tenant"}, {"__name__", "x"}}, Samples: 1},
	})
	stamped, err := injectTenantID(body, "second-tenant")
	if err != nil {
		t.Fatalf("inject on already-stamped: %v", err)
	}
	// readTenantIDFromFirstSeries returns the FIRST tenant_id in
	// label order. Since we PREPEND, the new tenant_id should win.
	got, found := readTenantIDFromFirstSeries(stamped)
	if !found {
		t.Fatalf("tenant_id should be present")
	}
	if got != "second-tenant" {
		t.Errorf("prepended tenant_id should win, got %q", got)
	}
}

func TestReadTenantIDFromFirstSeries_Present(t *testing.T) {
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "up"}, {TenantIDLabelName, "tenant-foo"}, {"job", "node"}}, Samples: 1},
	})
	got, found := readTenantIDFromFirstSeries(body)
	if !found {
		t.Fatalf("expected tenant_id to be found")
	}
	if got != "tenant-foo" {
		t.Errorf("expected tenant-foo, got %q", got)
	}
}

func TestReadTenantIDFromFirstSeries_Absent(t *testing.T) {
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "up"}, {"job", "node"}}, Samples: 1},
	})
	_, found := readTenantIDFromFirstSeries(body)
	if found {
		t.Errorf("tenant_id absent, found should be false")
	}
}

func TestReadTenantIDFromFirstSeries_EmptyWriteRequest(t *testing.T) {
	_, found := readTenantIDFromFirstSeries(nil)
	if found {
		t.Errorf("empty request, found should be false")
	}
}

func TestReadTenantIDFromFirstSeries_OnlyFirstSeriesInspected(t *testing.T) {
	// Edge case documented in the function comment: if tenant_id is
	// present in the SECOND timeseries but not the first, we report
	// "absent". This is consistent with the assumption that all
	// timeseries in one batch share external_labels.
	body := buildWriteRequestRich([]struct {
		Labels  [][2]string
		Samples int
	}{
		{Labels: [][2]string{{"__name__", "no-tenant"}}, Samples: 1},
		{Labels: [][2]string{{TenantIDLabelName, "stamped-only-on-second"}}, Samples: 1},
	})
	_, found := readTenantIDFromFirstSeries(body)
	if found {
		t.Errorf("non-uniform tenant_id labeling, first series wins (absent), got found=true")
	}
}
