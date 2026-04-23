package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Molten-Bot/moltenhub-code/internal/config"
)

const localSubmissionDedupTTL = 2 * time.Hour

type duplicateSubmissionError struct {
	requestID string
	state     string
}

func newDuplicateSubmissionError(requestID, state string) error {
	requestID = strings.TrimSpace(requestID)
	state = strings.TrimSpace(state)
	return &duplicateSubmissionError{
		requestID: requestID,
		state:     state,
	}
}

func (e *duplicateSubmissionError) Error() string {
	if e == nil {
		return "duplicate submission ignored"
	}
	if e.requestID == "" && e.state == "" {
		return "duplicate submission ignored"
	}
	if e.requestID == "" {
		return fmt.Sprintf("duplicate submission ignored (state=%s)", e.state)
	}
	if e.state == "" {
		return fmt.Sprintf("duplicate submission ignored (request_id=%s)", e.requestID)
	}
	return fmt.Sprintf("duplicate submission ignored (request_id=%s state=%s)", e.requestID, e.state)
}

func (e *duplicateSubmissionError) DuplicateRequestID() string {
	if e == nil {
		return ""
	}
	return e.requestID
}

func (e *duplicateSubmissionError) DuplicateState() string {
	if e == nil {
		return ""
	}
	return e.state
}

type localSubmissionDeduper struct {
	mu        sync.Mutex
	inFlight  map[string]string
	completed map[string]dedupeRecord
	ttl       time.Duration
}

type dedupeRecord struct {
	requestID   string
	completedAt time.Time
}

func newLocalSubmissionDeduper(ttl time.Duration) *localSubmissionDeduper {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &localSubmissionDeduper{
		inFlight:  map[string]string{},
		completed: map[string]dedupeRecord{},
		ttl:       ttl,
	}
}

func (d *localSubmissionDeduper) Check(key string, allowCompleted bool) (bool, string, string) {
	if d == nil {
		return false, "", ""
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false, "", ""
	}

	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gcLocked(now)

	if requestID, exists := d.inFlight[key]; exists {
		return true, "in_flight", requestID
	}
	if allowCompleted {
		return false, "accepted", ""
	}
	if record, exists := d.completed[key]; exists {
		return true, "completed", record.requestID
	}
	return false, "accepted", ""
}

func (d *localSubmissionDeduper) Begin(key, requestID string, allowCompleted bool) (bool, string, string) {
	if d == nil {
		return true, "accepted", ""
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return true, "accepted", ""
	}
	requestID = strings.TrimSpace(requestID)

	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gcLocked(now)

	if existingRequestID, exists := d.inFlight[key]; exists {
		return false, "in_flight", existingRequestID
	}
	if allowCompleted {
		d.inFlight[key] = requestID
		return true, "accepted", ""
	}
	if existingRecord, exists := d.completed[key]; exists {
		return false, "completed", existingRecord.requestID
	}
	d.inFlight[key] = requestID
	return true, "accepted", ""
}

func (d *localSubmissionDeduper) Done(key, requestID, finalState string) {
	if d == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	requestID = strings.TrimSpace(requestID)
	finalState = strings.TrimSpace(finalState)

	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()

	if existingRequestID, exists := d.inFlight[key]; exists {
		delete(d.inFlight, key)
		if existingRequestID != "" {
			requestID = existingRequestID
		}
	}
	if finalState == "error" {
		d.gcLocked(now)
		return
	}
	if requestID != "" {
		d.completed[key] = dedupeRecord{
			requestID:   requestID,
			completedAt: now,
		}
	}
	d.gcLocked(now)
}

func (d *localSubmissionDeduper) gcLocked(now time.Time) {
	if d == nil || d.ttl <= 0 {
		return
	}
	for key, record := range d.completed {
		if now.Sub(record.completedAt) > d.ttl {
			delete(d.completed, key)
		}
	}
}

func dedupeKeyForRunConfig(cfg config.Config) string {
	return config.DedupeKey(cfg)
}

func dedupeKeyForSubmission(cfg config.Config, source string) string {
	if !isAutomaticFollowUpSource(source) {
		return dedupeKeyForRunConfig(cfg)
	}

	normalized := cfg
	normalized.Prompt = strings.TrimSpace(strings.ToLower(source))
	return dedupeKeyForRunConfig(normalized)
}

func dedupeFinalStateForSubmission(source, finalState string) string {
	finalState = strings.TrimSpace(finalState)
	if !isAutomaticFollowUpSource(source) {
		return finalState
	}
	if strings.EqualFold(finalState, "error") {
		return "completed"
	}
	return finalState
}

func isAutomaticFollowUpSource(source string) bool {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case failureFollowUpSource, noChangesFollowUpSource, noChangesEscalationSource:
		return true
	default:
		return false
	}
}
