package api

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// injectTenantID rewrites the decoded WriteRequest, prepending a
// `tenant_id=<id>` Label record to every TimeSeries. This is the
// receiver's auto-stamp fallback path: invoked only when the client
// didn't already stamp the label (Day 4.2 will make the agent stamp
// proactively, after which this code path becomes rare — used only
// for legacy agents or external Prom installs that haven't yet
// added external_labels.tenant_id).
//
// Protobuf preserves field order semantics for `repeated` fields,
// so prepending the new Label is wire-compatible: VictoriaMetrics
// reads labels as a set regardless of position. We prepend (rather
// than append) to make the label visible to the cheap-validate path
// — readTenantIDFromFirstSeries walks until the first match, so
// putting tenant_id first means future round-trips short-circuit
// on the very first label.
//
// Pass-through for non-TimeSeries top-level fields (WriteRequest.metadata
// introduced in newer Prom versions, etc.) is byte-verbatim — we don't
// re-encode fields we don't touch. This preserves any unknown / future
// fields the producer included.
//
// Returns the rewritten decoded payload. Caller is responsible for
// re-snappy-encoding before forwarding to VM.
func injectTenantID(decoded []byte, tenantID string) ([]byte, error) {
	const (
		fieldTimeSeries = 1
		fieldLabels     = 1 // TimeSeries.labels
		fieldLabelName  = 1
		fieldLabelValue = 2
	)
	// Pre-encode the new label payload once — same bytes appended to
	// every TimeSeries so we avoid reallocating per-iteration.
	//
	// Label encoding:
	//   tag(1, BytesType) length-prefix "tenant_id"
	//   tag(2, BytesType) length-prefix <id>
	var labelBody []byte
	labelBody = protowire.AppendTag(labelBody, fieldLabelName, protowire.BytesType)
	labelBody = protowire.AppendString(labelBody, TenantIDLabelName)
	labelBody = protowire.AppendTag(labelBody, fieldLabelValue, protowire.BytesType)
	labelBody = protowire.AppendString(labelBody, tenantID)

	// TimeSeries.labels entry wrapper: tag(1, BytesType) + length-prefix(labelBody)
	var labelEntry []byte
	labelEntry = protowire.AppendTag(labelEntry, fieldLabels, protowire.BytesType)
	labelEntry = protowire.AppendBytes(labelEntry, labelBody)

	// Conservative size estimate: input plus N extra bytes per
	// TimeSeries for the inserted label entry. We don't know N in
	// advance; +20% headroom is plenty for typical batches.
	out := make([]byte, 0, len(decoded)+len(decoded)/5)
	rem := decoded
	startLen := len(rem)

	for len(rem) > 0 {
		preTagLen := startLen - len(rem)
		_ = preTagLen
		// Capture the start of THIS field in the original slice so we
		// can copy unchanged fields byte-verbatim.
		fieldStartOffset := startLen - len(rem)

		num, typ, tagLen := protowire.ConsumeTag(rem)
		if tagLen < 0 {
			return nil, fmt.Errorf("injectTenantID tag: %w", protowire.ParseError(tagLen))
		}
		afterTag := rem[tagLen:]

		if num == fieldTimeSeries && typ == protowire.BytesType {
			tsBytes, n := protowire.ConsumeBytes(afterTag)
			if n < 0 {
				return nil, fmt.Errorf("injectTenantID ts bytes: %w", protowire.ParseError(n))
			}
			// New TimeSeries body = labelEntry (prepended) + original body.
			// Allocate fresh; cannot use append on tsBytes because that
			// could clobber the input slice on capacity overlap.
			newTS := make([]byte, 0, len(tsBytes)+len(labelEntry))
			newTS = append(newTS, labelEntry...)
			newTS = append(newTS, tsBytes...)

			out = protowire.AppendTag(out, fieldTimeSeries, protowire.BytesType)
			out = protowire.AppendBytes(out, newTS)
			rem = afterTag[n:]
			continue
		}

		// Non-TimeSeries field — pass through verbatim (tag + value).
		valLen := protowire.ConsumeFieldValue(num, typ, afterTag)
		if valLen < 0 {
			return nil, fmt.Errorf("injectTenantID skip: %w", protowire.ParseError(valLen))
		}
		totalFieldLen := tagLen + valLen
		out = append(out, decoded[fieldStartOffset:fieldStartOffset+totalFieldLen]...)
		rem = afterTag[valLen:]
	}

	return out, nil
}
