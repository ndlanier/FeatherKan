package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Entry struct {
	Author string `json:"author"`
	Text   string `json:"text"`
	At     string `json:"at"`
}
type Task struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Assignee    string  `json:"assignee"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Revision    int     `json:"revision"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
	Comments    []Entry `json:"comments"`
	History     []Entry `json:"history"`
}
type Input struct {
	ID          string  `json:"id"`
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Assignee    *string `json:"assignee,omitempty"`
	Status      *string `json:"status,omitempty"`
	Revision    int     `json:"revision,omitempty"`
	Actor       string  `json:"actor,omitempty"`
	Text        string  `json:"text,omitempty"`
	Query       string  `json:"query,omitempty"`
}
type apiError struct {
	Code    int
	Message string
}

func (e *apiError) Error() string     { return e.Message }
func fail(code int, msg string) error { return &apiError{code, msg} }

type Store struct {
	mu    sync.Mutex
	path  string
	tasks []Task
	lock  *os.File
}

func openStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "board.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("data directory is already in use: %w", err)
	}
	s := &Store{path: filepath.Join(dir, "board.json"), tasks: []Task{}, lock: f}
	b, err := os.ReadFile(s.path)
	if err == nil {
		err = json.Unmarshal(b, &s.tasks)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.close()
		return nil, fmt.Errorf("read board: %w", err)
	}
	if s.tasks == nil {
		s.tasks = []Task{}
	}
	return s, nil
}
func (s *Store) close() { syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN); s.lock.Close() }
func clone(tasks []Task) []Task {
	b, _ := json.Marshal(tasks)
	var out []Task
	json.Unmarshal(b, &out)
	return out
}
func (s *Store) save(tasks []Task) error {
	b, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".board-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	// Rename is the commit point; synchronize its directory entry where supported.
	if d, e := os.Open(filepath.Dir(s.path)); e == nil {
		_ = d.Sync()
		d.Close()
	}
	s.tasks = tasks
	return nil
}
func validStatus(s string) bool {
	return s == "backlog" || s == "in_progress" || s == "blocked" || s == "done"
}
func (s *Store) run(action string, in Input) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks := clone(s.tasks)
	if action == "list_tasks" {
		out := []Task{}
		q := strings.ToLower(in.Query)
		for _, t := range tasks {
			b, _ := json.Marshal(t)
			if (in.Status == nil || t.Status == *in.Status) && (in.Assignee == nil || t.Assignee == *in.Assignee) && strings.Contains(strings.ToLower(string(b)), q) {
				out = append(out, t)
			}
		}
		return out, nil
	}
	idx := -1
	for i := range tasks {
		if tasks[i].ID == in.ID {
			idx = i
			break
		}
	}
	if action == "get_task" {
		if idx < 0 {
			return nil, fail(404, "Task not found")
		}
		return tasks[idx], nil
	}
	in.Actor = strings.TrimSpace(in.Actor)
	if in.Actor == "" || len(in.Actor) > 120 {
		return nil, fail(400, "actor is required (maximum 120 characters)")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if action == "create_task" {
		if in.Title == nil || strings.TrimSpace(*in.Title) == "" || len(*in.Title) > 200 {
			return nil, fail(400, "title is required (maximum 200 characters)")
		}
		t := Task{ID: fmt.Sprintf("%x", randomBytes()), Title: strings.TrimSpace(*in.Title), Status: "backlog", Revision: 1, CreatedAt: now, UpdatedAt: now, Comments: []Entry{}, History: []Entry{{in.Actor, "Created task", now}}}
		if in.Description != nil {
			t.Description = *in.Description
		}
		if len(t.Description) > 30000 {
			return nil, fail(400, "description too long")
		}
		if in.Assignee != nil {
			t.Assignee = strings.TrimSpace(*in.Assignee)
		}
		if len(t.Assignee) > 120 {
			return nil, fail(400, "assignee too long")
		}
		if in.Status != nil {
			t.Status = *in.Status
		}
		if !validStatus(t.Status) {
			return nil, fail(400, "Invalid status")
		}
		if t.Status == "in_progress" && t.Assignee == "" {
			return nil, fail(400, "Assign an owner before starting work")
		}
		tasks = append(tasks, t)
		if err := s.save(tasks); err != nil {
			return nil, err
		}
		return t, nil
	}
	if idx < 0 {
		return nil, fail(404, "Task not found")
	}
	t := &tasks[idx]
	// Comments append under the same lock without requiring a stale form to be resaved.
	if action == "add_comment" {
		if strings.TrimSpace(in.Text) == "" || len(in.Text) > 10000 {
			return nil, fail(400, "Comment is required (maximum 10000 characters)")
		}
		t.Comments = append(t.Comments, Entry{in.Actor, in.Text, now})
	} else {
		if in.Revision != t.Revision {
			return nil, fail(409, "Task changed. Reload it and review the latest revision before retrying.")
		}
		switch action {
		case "claim_task":
			if t.Assignee != "" && t.Assignee != in.Actor {
				return nil, fail(409, "Task is already assigned to "+t.Assignee)
			}
			if t.Status == "done" {
				return nil, fail(409, "Reopen this task before claiming it")
			}
			t.Assignee = in.Actor
			t.Status = "in_progress"
			t.History = append(t.History, Entry{in.Actor, "Claimed task and started work", now})
		case "release_task":
			if t.Assignee != in.Actor {
				return nil, fail(409, "Only the current assignee can release this task")
			}
			t.Assignee = ""
			t.Status = "backlog"
			t.History = append(t.History, Entry{in.Actor, "Released task to backlog", now})
		case "update_task":
			if t.Assignee != "" && t.Assignee != in.Actor {
				return nil, fail(409, "Task belongs to "+t.Assignee+". Coordinate with its owner before editing.")
			}
			before := *t
			if in.Title != nil {
				t.Title = strings.TrimSpace(*in.Title)
			}
			if in.Description != nil {
				t.Description = *in.Description
			}
			if in.Status != nil {
				t.Status = *in.Status
			}
			if in.Assignee != nil {
				t.Assignee = strings.TrimSpace(*in.Assignee)
			}
			if t.Title == "" || len(t.Title) > 200 || len(t.Description) > 30000 || len(t.Assignee) > 120 || !validStatus(t.Status) {
				return nil, fail(400, "Invalid task fields")
			}
			if t.Status == "in_progress" && t.Assignee == "" {
				return nil, fail(400, "Assign an owner before starting work")
			}
			changes := []string{}
			if before.Title != t.Title {
				changes = append(changes, "title updated")
			}
			if before.Description != t.Description {
				changes = append(changes, "description updated")
			}
			if before.Status != t.Status {
				changes = append(changes, "status: "+before.Status+" → "+t.Status)
			}
			if before.Assignee != t.Assignee {
				changes = append(changes, "assignee: "+before.Assignee+" → "+t.Assignee)
			}
			if len(changes) == 0 {
				return *t, nil
			}
			t.History = append(t.History, Entry{in.Actor, strings.Join(changes, "; "), now})
		default:
			return nil, fail(400, "Unknown operation")
		}
	}
	t.Revision++
	t.UpdatedAt = now
	result := *t
	if err := s.save(tasks); err != nil {
		return nil, err
	}
	return result, nil
}
func randomBytes() []byte {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
