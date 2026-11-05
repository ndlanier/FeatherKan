package main

import (
	"fmt"
	"strings"
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
	Requests []Request `json:"requests"`
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
