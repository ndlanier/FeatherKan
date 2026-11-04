package main

import (
	"fmt"
	"strings"
	"time"
)

type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type Queue struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MaxInFlight int    `json:"maxInFlight"`
}
type State struct {
	Version  int       `json:"version"`
	Projects []Project `json:"projects"`
	Queues   []Queue   `json:"queues"`
	Tasks    []Task    `json:"tasks"`
}

func (s *Store) hasProject(id string) bool {
	for _, p := range s.projects {
		if p.ID == id {
			return true
		}
	}
	return false
}
func (s *Store) findQueue(project, id string) *Queue {
	for _, q := range s.queues {
		if q.ProjectID == project && q.ID == id {
			return &q
		}
	}
	return nil
}
func (s *Store) validateState() error {
	if !s.hasProject("default") {
		return fmt.Errorf("default project missing")
	}
	seen := map[string]bool{}
	for _, p := range s.projects {
		if p.ID == "" || seen[p.ID] {
			return fmt.Errorf("invalid project ID")
		}
		seen[p.ID] = true
	}
	seen = map[string]bool{}
	for _, q := range s.queues {
		if q.ID == "" || seen[q.ID] || !s.hasProject(q.ProjectID) || q.MaxInFlight < 1 || q.MaxInFlight > 100 {
			return fmt.Errorf("invalid queue")
		}
		seen[q.ID] = true
	}
	seen = map[string]bool{}
	for _, t := range s.tasks {
		if t.ID == "" || seen[t.ID] || !s.hasProject(t.ProjectID) || (t.QueueID != "" && s.findQueue(t.ProjectID, t.QueueID) == nil) {
			return fmt.Errorf("invalid task references")
		}
		seen[t.ID] = true
	}
	if s.queues == nil {
		s.queues = []Queue{}
	}
	return nil
}
func metadata(in Input) (string, string, error) {
	name := strings.TrimSpace(in.Name)
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	if name == "" || len(name) > 120 || len(description) > 30000 {
		return "", "", fail(400, "Name is required (120 bytes maximum); description maximum is 30000 bytes")
	}
	return name, description, nil
}
func (s *Store) createProject(in Input, admin bool) (any, error) {
	if !admin {
		return nil, fail(403, "Administrator access required")
	}
	name, description, err := metadata(in)
	if err != nil {
		return nil, err
	}
	for _, p := range s.projects {
		if strings.EqualFold(p.Name, name) {
			return nil, fail(409, "A project with this name already exists")
		}
	}
	p := Project{fmt.Sprintf("%x", randomBytes()), name, description}
	projects := append(append([]Project{}, s.projects...), p)
	if err := s.saveState(s.tasks, projects, s.queues); err != nil {
		return nil, err
	}
	return p, nil
}
func (s *Store) createQueue(in Input, admin bool) (any, error) {
	if !admin {
		return nil, fail(403, "Administrator access required")
	}
	name, description, err := metadata(in)
	if err != nil {
		return nil, err
	}
	for _, q := range s.queues {
		if q.ProjectID == in.ProjectID && strings.EqualFold(q.Name, name) {
			return nil, fail(409, "A queue with this name already exists in this project")
		}
	}
	limit := in.MaxInFlight
	if limit == 0 {
		limit = 1
	}
	if limit < 1 || limit > 100 {
		return nil, fail(400, "maxInFlight must be between 1 and 100")
	}
	q := Queue{fmt.Sprintf("%x", randomBytes()), in.ProjectID, name, description, limit}
	queues := append(append([]Queue{}, s.queues...), q)
	if err := s.saveState(s.tasks, s.projects, queues); err != nil {
		return nil, err
	}
	return q, nil
}
func (s *Store) queueCapacity(tasks []Task, t Task) error {
	if t.QueueID == "" {
		return nil
	}
	q := s.findQueue(t.ProjectID, t.QueueID)
	if q == nil {
		return fail(404, "Queue not found")
	}
	active := 0
	for _, other := range tasks {
		if other.ID != t.ID && other.ProjectID == t.ProjectID && other.QueueID == t.QueueID && other.Status == "in_progress" {
			active++
		}
	}
	if active >= q.MaxInFlight {
		return fail(409, "Queue is at its active-work limit")
	}
	return nil
}

// Called under Store.mu: selection and persistence are one atomic operation.
func (s *Store) claimNext(tasks []Task, in Input) (any, error) {
	in.Actor = strings.TrimSpace(in.Actor)
	if in.Actor == "" || len(in.Actor) > 120 {
		return nil, fail(400, "actor is required (120 bytes maximum)")
	}
	if in.QueueID == nil || *in.QueueID == "" {
		return nil, fail(400, "queueId is required")
	}
	queue := s.findQueue(in.ProjectID, *in.QueueID)
	if queue == nil {
		return nil, fail(404, "Queue not found")
	}
	active := 0
	for _, t := range tasks {
		if t.ProjectID == in.ProjectID && t.QueueID == queue.ID && t.Status == "in_progress" {
			if t.Assignee == in.Actor {
				return t, nil
			}
			active++
		}
	}
	if active >= queue.MaxInFlight {
		return nil, nil
	}
	idx := -1
	var oldest time.Time
	for i, t := range tasks {
		if t.ProjectID == in.ProjectID && t.QueueID == queue.ID && t.Status == "backlog" && (t.Assignee == "" || t.Assignee == in.Actor) {
			enqueued, err := time.Parse(time.RFC3339Nano, t.EnqueuedAt)
			if err != nil {
				return nil, fmt.Errorf("invalid enqueue timestamp for task %s: %w", t.ID, err)
			}
			if idx < 0 || enqueued.Before(oldest) || (enqueued.Equal(oldest) && t.ID < tasks[idx].ID) {
				idx = i
				oldest = enqueued
			}
		}
	}
	if idx < 0 {
		return nil, nil
	}
	t := &tasks[idx]
	now := time.Now().UTC().Format(time.RFC3339Nano)
	t.Status = "in_progress"
	t.Assignee = in.Actor
	t.Revision++
	t.UpdatedAt = now
	t.History = append(t.History, Entry{in.Actor, "Claimed next item from queue " + queue.Name, now})
	if err := s.save(tasks); err != nil {
		return nil, err
	}
	return *t, nil
}
