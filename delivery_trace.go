package main

import (
	"sync"
	"time"
)

// Numeric/ID-only bounded evidence. Source timestamp is explicitly NOT a disk
// write time. No content, credentials or local paths are captured here.
type DeliveryTrace struct {
	EventID        string    `json:"event_id"`
	SourceID       string    `json:"source_file_id"`
	ConversationID string    `json:"conversation_id"`
	Offset         int64     `json:"offset"`
	Kind           string    `json:"kind"`
	SourceAt       time.Time `json:"source_timestamp"`
	ObservedAt     time.Time `json:"observed_at"`
	QueuedAt       time.Time `json:"queued_at"`
	SentAt         time.Time `json:"sent_at"`
	ReceivedAt     time.Time `json:"received_at"`
	CommittedAt    time.Time `json:"committed_at"`
	LockMS         float64   `json:"lock_ms"`
	CommitMS       float64   `json:"commit_ms"`
}
type deliveryTraces struct {
	sync.Mutex
	entries []DeliveryTrace
}

func (d *deliveryTraces) add(events []UsageEvent, received, committed time.Time, lockMS, commitMS float64) {
	d.Lock()
	defer d.Unlock()
	for _, e := range events {
		value := DeliveryTrace{EventID: e.EventID, SourceID: e.SourceFileID, Offset: e.ByteOffset, ConversationID: e.ConversationID, Kind: e.EventType, SourceAt: e.Timestamp}
		if e.Trace != nil {
			value.ObservedAt = e.Trace.ObservedAt
			value.QueuedAt = e.Trace.QueuedAt
			value.SentAt = e.Trace.SentAt
		}
		value.ReceivedAt = received
		value.CommittedAt = committed
		value.LockMS = lockMS
		value.CommitMS = commitMS
		d.entries = append(d.entries, value)
	}
	if len(d.entries) > 64 {
		d.entries = append([]DeliveryTrace(nil), d.entries[len(d.entries)-64:]...)
	}
}
func (d *deliveryTraces) recent() []DeliveryTrace {
	d.Lock()
	defer d.Unlock()
	return append([]DeliveryTrace(nil), d.entries...)
}
