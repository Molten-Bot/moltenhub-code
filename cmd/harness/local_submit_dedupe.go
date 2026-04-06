package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jef/moltenhub-code/internal/config"
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

func (d *localSubmissionDeduper) Check(key string) (bool, string, string) {
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
	if record, exists := d.completed[key]; exists {
		return true, "completed", record.requestID
	}
	return false, "accepted", ""
}

func (d *localSubmissionDeduper) Begin(key, requestID string) (bool, string, string) {
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
	baseBranch := normalizeBranchRefForDeduper(cfg.BaseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}

	repos := normalizeRepoList(cfg.RepoList())
	var (
		encoded []byte
		err     error
	)
	if baseBranch == "main" {
		payload := struct {
			Prompt     string   `json:"prompt"`
			Repos      []string `json:"repos"`
			BaseBranch string   `json:"base_branch"`
			Images     []string `json:"images,omitempty"`
		}{
			Prompt:     strings.TrimSpace(cfg.Prompt),
			Repos:      repos,
			BaseBranch: baseBranch,
			Images:     promptImageFingerprints(cfg.Images),
		}
		encoded, err = json.Marshal(payload)
	} else {
		payload := struct {
			Repos        []string `json:"repos"`
			TargetBranch string   `json:"target_branch"`
			Images       []string `json:"images,omitempty"`
		}{
			Repos:        repos,
			TargetBranch: baseBranch,
			Images:       promptImageFingerprints(cfg.Images),
		}
		encoded, err = json.Marshal(payload)
	}
	if err != nil {
		return ""
	}
	return string(encoded)
}

func normalizeBranchRefForDeduper(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/heads/")
	branch = strings.TrimPrefix(branch, "origin/")
	return branch
}

func normalizeRepoList(repos []string) []string {
	if len(repos) == 0 {
		return nil
	}
	out := make([]string, 0, len(repos))
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		out = append(out, repo)
	}
	return out
}

func promptImageFingerprints(images []config.PromptImage) []string {
	if len(images) == 0 {
		return nil
	}
	out := make([]string, 0, len(images))
	for _, image := range images {
		sum := sha256.Sum256([]byte(strings.Join([]string{
			strings.TrimSpace(image.Name),
			strings.TrimSpace(image.MediaType),
			strings.TrimSpace(image.DataBase64),
		}, "\n")))
		out = append(out, hex.EncodeToString(sum[:]))
	}
	return out
}
