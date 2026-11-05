package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Requests are opaque supervisor messages, independent of Kanban tasks.
type Request struct {
	ID         string          `json:"id"`
	ProjectID  string          `json:"projectId"`
	QueueID    string          `json:"queueId"`
	Payload    json.RawMessage `json:"payload"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	Status     string          `json:"status"`
	ClaimedBy  string          `json:"claimedBy"`
	CreatedBy  string          `json:"createdBy"`
	Revision   int             `json:"revision"`
	CreatedAt  string          `json:"createdAt"`
	EnqueuedAt string          `json:"enqueuedAt"`
	UpdatedAt  string          `json:"updatedAt"`
	History    []Entry         `json:"history"`
}

func backupSchema(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
func (s *Store) validateRequests() error {
	seen := map[string]bool{}
	for _, r := range s.requests {
		if r.ID == "" || seen[r.ID] || !s.hasProject(r.ProjectID) || s.findQueue(r.ProjectID, r.QueueID) == nil || !json.Valid(r.Payload) || r.Revision < 1 {
			return fmt.Errorf("invalid request references or data")
		}
		if r.Status != "pending" && r.Status != "active" && r.Status != "completed" && r.Status != "failed" {
			return fmt.Errorf("invalid request status")
		}
		if r.Status == "active" && r.ClaimedBy == "" {
			return fmt.Errorf("active request missing owner")
		}
		if _, err := time.Parse(time.RFC3339Nano, r.EnqueuedAt); err != nil {
			return err
		}
		seen[r.ID] = true
	}
	return nil
}
func validJSONData(data json.RawMessage) bool {
	return len(data) > 0 && len(data) <= 30000 && json.Valid(data)
}

// runRequest executes under Store.mu; failed persistence never changes live state.
func (s *Store) runRequest(action string, in Input, admin bool) (any, error) {
	if in.QueueID == nil || *in.QueueID == "" {
		return nil, fail(400, "queueId is required")
	}
	q := s.findQueue(in.ProjectID, *in.QueueID)
	if q == nil {
		return nil, fail(404, "Queue not found in this project")
	}
	b, _ := json.Marshal(s.requests)
	requests := []Request{}
	json.Unmarshal(b, &requests)
	if action == "list_requests" {
		out := []Request{}
		for _, r := range requests {
			if r.ProjectID == in.ProjectID && r.QueueID == q.ID && (in.Status == nil || r.Status == *in.Status) {
				out = append(out, r)
			}
		}
		return out, nil
	}
	idx := -1
	for i, r := range requests {
		if r.ID == in.ID && r.ProjectID == in.ProjectID && r.QueueID == q.ID {
			idx = i
			break
		}
	}
	if action == "get_request" {
		if idx < 0 {
			return nil, fail(404, "Request not found")
		}
		return requests[idx], nil
	}
	in.Actor = strings.TrimSpace(in.Actor)
	if in.Actor == "" || len(in.Actor) > 120 {
		return nil, fail(400, "actor is required (120 bytes maximum)")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if action == "enqueue_request" {
		if !validJSONData(in.Payload) {
			return nil, fail(400, "payload must be valid JSON (30000 bytes maximum)")
		}
		r := Request{ID: fmt.Sprintf("%x", randomBytes()), ProjectID: in.ProjectID, QueueID: q.ID, Payload: append(json.RawMessage{}, in.Payload...), Status: "pending", CreatedBy: in.Actor, Revision: 1, CreatedAt: now, EnqueuedAt: now, UpdatedAt: now, History: []Entry{{in.Actor, "Enqueued request", now}}}
		requests = append(requests, r)
		if err := s.persist(s.tasks, s.projects, s.queues, requests); err != nil {
			return nil, err
		}
		return r, nil
	}
	if action == "claim_next" {
		active := 0
		for _, r := range requests {
			if r.ProjectID == in.ProjectID && r.QueueID == q.ID && r.Status == "active" {
				if r.ClaimedBy == in.Actor {
					return r, nil
				}
				active++
			}
		}
		if active >= q.MaxInFlight {
			return nil, nil
		}
		idx = -1
		var oldest time.Time
		for i, r := range requests {
			if r.ProjectID == in.ProjectID && r.QueueID == q.ID && r.Status == "pending" {
				at, err := time.Parse(time.RFC3339Nano, r.EnqueuedAt)
				if err != nil {
					return nil, err
				}
				if idx < 0 || at.Before(oldest) || (at.Equal(oldest) && r.ID < requests[idx].ID) {
					idx = i
					oldest = at
				}
			}
		}
		if idx < 0 {
			return nil, nil
		}
		requests[idx].Status = "active"
		requests[idx].ClaimedBy = in.Actor
	} else {
		if idx < 0 {
			return nil, fail(404, "Request not found")
		}
		r := &requests[idx]
		if r.Revision != in.Revision {
			return nil, fail(409, "Request changed. Read the latest revision before retrying.")
		}
		if !admin && r.ClaimedBy != in.Actor {
			return nil, fail(409, "Request belongs to another supervisor")
		}
		switch action {
		case "complete_request", "fail_request":
			if r.Status != "active" {
				return nil, fail(409, "Only active requests can be completed or failed")
			}
			if len(in.Result) > 0 && !validJSONData(in.Result) {
				return nil, fail(400, "result must be valid JSON (30000 bytes maximum)")
			}
			r.Result = append(json.RawMessage(nil), in.Result...)
			if action == "complete_request" {
				r.Status = "completed"
				r.Error = ""
			} else {
				if strings.TrimSpace(in.Text) == "" || len(in.Text) > 10000 {
					return nil, fail(400, "text must describe the failure (10000 bytes maximum)")
				}
				r.Status = "failed"
				r.Error = in.Text
			}
		case "release_request":
			if r.Status != "active" && r.Status != "failed" {
				return nil, fail(409, "Only active or failed requests can be released")
			}
			r.Status = "pending"
			r.ClaimedBy = ""
			r.EnqueuedAt = now
			r.Result = nil
			r.Error = ""
		default:
			return nil, fail(400, "Unknown request operation")
		}
	}
	r := &requests[idx]
	r.Revision++
	r.UpdatedAt = now
	entry := action
	if admin {
		entry = "Administrator action: " + entry
	}
	if action == "fail_request" {
		entry += " — " + in.Text
	}
	r.History = append(r.History, Entry{in.Actor, entry, now})
	if err := s.persist(s.tasks, s.projects, s.queues, requests); err != nil {
		return nil, err
	}
	return *r, nil
}
