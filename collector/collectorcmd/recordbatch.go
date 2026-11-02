package collectorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
)

// Records payloads that fit the edge (COL-08, COL-05). The edge refuses a
// message larger than its maximum payload (NATS default 1 MiB), and the
// HTTP path authenticates no collector, so the records of a discovery run
// go in payloads below that size, in their order: uploadRecordsAt splits a
// run, flushSpool splits a spooled batch the edge refused as too large
// (spooled before this split existed, or for an edge with a smaller limit)
// instead of retrying it forever ahead of every later batch. A single
// record larger than the edge's limit is quarantined and counted as lost;
// it never blocks the spool. Only the edge's own limit decides that: while
// it is unknown (no connection), such a record is spooled in a payload of
// its own, and flushSpool quarantines it only if the edge refuses it. The
// server deduplicates records on record_id, so a payload delivered twice
// after a crash does no harm.

// defaultRecordPayloadLimit is the size of a records payload while the
// edge's limit is unknown (no connection): half the NATS default, so a
// payload spooled now fits the edge later. It is a packing size only,
// never a reason to quarantine a record.
const defaultRecordPayloadLimit = 512 << 10

// recordPayloadLimit returns the size a records payload may have and
// whether it is the edge's own limit: the edge's maximum message size
// while the collector holds its channel (known), else
// defaultRecordPayloadLimit (not known); tests replace it.
var recordPayloadLimit = func() (limit int, known bool) {
	if ch := scopeChannel.Load(); ch != nil && ch.nc != nil && ch.nc.IsConnected() {
		if n := ch.nc.MaxPayload(); n > 0 {
			return int(n), true
		}
	}
	return defaultRecordPayloadLimit, false
}

// errRecordTooLarge: a device record does not fit a payload of its own; it
// is quarantined and counted as lost.
var errRecordTooLarge = errors.New("device record larger than the maximum message size of the edge; quarantined and counted as lost")

// The encoding of plugins.DeviceRecordBatch around its records.
const (
	recordsPrefix = `{"records":[`
	recordsSuffix = `]}`
)

// recordsPayload is one records payload and the number of records in it.
type recordsPayload struct {
	data    []byte
	records int
}

// partSourceTime is the spool time of the i-th payload of a run: the
// source time of the run plus i nanoseconds. The spool delivers by this
// time (message ids sort by it), so the payloads of a run keep their order
// and stay ahead of every later run (COL-05).
func partSourceTime(sourceTime time.Time, i int) time.Time {
	return sourceTime.Add(time.Duration(i))
}

// marshalRecords encodes the records of a run one by one, as
// plugins.DeviceRecordBatch encodes its elements.
func marshalRecords(records []plugins.DeviceRecord) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(records))
	for i := range records {
		raw, err := json.Marshal(&records[i])
		if err != nil {
			return nil, fmt.Errorf("marshal records: %w", err)
		}
		out[i] = raw
	}
	return out, nil
}

// splitRecords packs the encoded records, in their order, into payloads
// (the encoding of plugins.DeviceRecordBatch) of at most limit bytes. A
// record that does not fit a payload of its own goes in a payload of its
// own: when limit is the edge's own limit (known), it is returned in
// oversized and left out of the others; else it stays in payloads, in its
// place, and the edge decides once it is connected.
func splitRecords(raws []json.RawMessage, limit int, known bool) (payloads, oversized []recordsPayload) {
	overhead := len(recordsPrefix) + len(recordsSuffix)
	var current []json.RawMessage
	size := overhead
	flush := func() {
		if len(current) > 0 {
			payloads = append(payloads, recordsPayload{data: joinRecords(current), records: len(current)})
			current, size = nil, overhead
		}
	}
	for _, raw := range raws {
		if overhead+len(raw) > limit {
			single := recordsPayload{data: joinRecords([]json.RawMessage{raw}), records: 1}
			if known {
				oversized = append(oversized, single)
			} else {
				flush()
				payloads = append(payloads, single)
			}
			continue
		}
		if len(current) > 0 && size+1+len(raw) > limit {
			flush()
		}
		if len(current) > 0 {
			size++ // the comma
		}
		current = append(current, raw)
		size += len(raw)
	}
	flush()
	return payloads, oversized
}

// joinRecords encodes raws as a records payload.
func joinRecords(raws []json.RawMessage) []byte {
	n := len(recordsPrefix) + len(recordsSuffix) + len(raws)
	for _, r := range raws {
		n += len(r)
	}
	buf := make([]byte, 0, n)
	buf = append(buf, recordsPrefix...)
	for i, r := range raws {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, r...)
	}
	return append(buf, recordsSuffix...)
}

// splitSpooledRecords splits a spooled records payload the edge refused as
// too large into payloads of at most limit bytes (splitRecords). When the
// limit lets the whole payload pass (the edge's limit is smaller than
// known), it is halved. A payload of a single record, or one that is not a
// records payload, cannot be split (ok false).
func splitSpooledRecords(payload []byte, limit int, known bool) (payloads, oversized []recordsPayload, ok bool) {
	var batch struct {
		Records []json.RawMessage `json:"records"`
	}
	if json.Unmarshal(payload, &batch) != nil || len(batch.Records) < 2 {
		return nil, nil, false
	}
	payloads, oversized = splitRecords(batch.Records, limit, known)
	if len(payloads) == 1 && len(oversized) == 0 {
		mid := len(batch.Records) / 2
		payloads = []recordsPayload{
			{data: joinRecords(batch.Records[:mid]), records: mid},
			{data: joinRecords(batch.Records[mid:]), records: len(batch.Records) - mid},
		}
	}
	return payloads, oversized, true
}

// quarantineOversized keeps a payload of one record that no edge message
// can carry in the spool's quarantine and counts it as lost.
func (u *uploader) quarantineOversized(ctx context.Context, p recordsPayload, sourceTime time.Time, limit int) {
	slog.Error("device record larger than the maximum message size of the edge; quarantined and counted as lost",
		"event", "collector.record.oversized", "bytes", len(p.data), "limit", limit)
	if u.spool == nil {
		return
	}
	if err := u.spool.Quarantine(ctx, &buffer.Message{Topic: spoolTopic, Payload: p.data, CreatedAt: sourceTime.UTC()}); err != nil {
		slog.Error("quarantining the oversized record failed", "error", err)
	}
}

// splitSpooledBatch replaces the spooled batch msg, which the edge refused
// as too large, by its parts with its source time (partSourceTime) in one
// step of the spool (buffer.DiskBuffer.Replace): the parts take the place
// of msg, so they keep their order and their place ahead of every later
// batch (COL-05), also when msg is itself a part. They take its space too,
// so the spool's size limit does not apply to them: a split never fails
// for a full spool, counts no loss and drops no batch, not even msg, to
// make room. msg is removed only once all parts are stored: a crash in
// between delivers records twice, which the server deduplicates, but loses
// none. The records no edge message can carry are quarantined and counted
// as lost after the split. A batch that cannot be split (a single record
// the edge refused) is quarantined and counted as lost.
func (u *uploader) splitSpooledBatch(ctx context.Context, msg *buffer.Message) error {
	limit, known := recordPayloadLimit()
	payloads, oversized, ok := splitSpooledRecords(msg.Payload, limit, known)
	if !ok {
		if err := u.spool.Reject(ctx, msg.ID); err != nil {
			return fmt.Errorf("quarantine the oversized spooled batch: %w", err)
		}
		slog.Error("spooled batch larger than the maximum message size of the edge and not splittable; quarantined and counted as lost",
			"event", "collector.record.oversized", "message_id", msg.ID, "bytes", len(msg.Payload), "limit", limit)
		return nil
	}
	parts := make([]buffer.Message, len(payloads))
	for i, p := range payloads {
		parts[i] = buffer.Message{Topic: spoolTopic, Payload: p.data, CreatedAt: partSourceTime(msg.CreatedAt, i)}
	}
	if _, err := u.spool.Replace(ctx, msg.ID, parts); err != nil {
		// The batch stays in the spool and is split again on the next flush.
		return fmt.Errorf("split the oversized spooled batch: %w", err)
	}
	for _, p := range oversized {
		u.quarantineOversized(ctx, p, msg.CreatedAt, limit)
	}
	slog.Info("spooled batch split for the edge's maximum message size", "event", "collector.spool.split",
		"message_id", msg.ID, "parts", len(payloads), "oversized_records", len(oversized), "limit", limit)
	return nil
}
