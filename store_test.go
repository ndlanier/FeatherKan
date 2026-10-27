package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func ptr(s string) *string { return &s }
func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := openStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.close)
	return s
}
func create(t *testing.T, s *Store) Task {
	t.Helper()
	v, e := s.run("create_task", Input{Title: ptr("Implement shared cache"), Actor: "planner", Description: ptr("Scope: cache.go. Keep compatibility.")})
	if e != nil {
		t.Fatal(e)
	}
	return v.(Task)
}
func TestConcurrentClaimOneWinner(t *testing.T) {
	s := testStore(t)
	task := create(t, s)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.run("claim_task", Input{ID: task.ID, Revision: 1, Actor: "worker"})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if e, ok := err.(*apiError); !ok || e.Code != 409 {
				t.Errorf("unexpected error %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("got %d successful claims, want 1", wins)
	}
}
func TestOwnershipRevisionHandoffAndComments(t *testing.T) {
	s := testStore(t)
	task := create(t, s)
	v, e := s.run("claim_task", Input{ID: task.ID, Revision: 1, Actor: "alpha"})
	if e != nil {
		t.Fatal(e)
	}
	task = v.(Task)
	if _, e = s.run("update_task", Input{ID: task.ID, Revision: task.Revision, Actor: "beta", Title: ptr("Overwritten")}); e == nil {
		t.Fatal("other owner overwrite allowed")
	}
	if _, e = s.run("claim_task", Input{ID: task.ID, Revision: task.Revision, Actor: "beta"}); e == nil {
		t.Fatal("other owner claim allowed")
	}
	v, e = s.run("add_comment", Input{ID: task.ID, Actor: "beta", Text: "Handoff context: touching cache.go"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.run("update_task", Input{ID: task.ID, Revision: task.Revision, Actor: "alpha", Status: ptr("done")}); e == nil {
		t.Fatal("stale write allowed")
	}
	task = v.(Task)
	v, e = s.run("update_task", Input{ID: task.ID, Revision: task.Revision, Actor: "alpha", Assignee: ptr("beta")})
	if e != nil {
		t.Fatal(e)
	}
	task = v.(Task)
	v, e = s.run("update_task", Input{ID: task.ID, Revision: task.Revision, Actor: "beta", Status: ptr("done")})
	if e != nil {
		t.Fatal(e)
	}
	task = v.(Task)
	if len(task.Comments) != 1 || len(task.History) != 4 {
		t.Fatalf("lost history: %+v", task)
	}
	v, e = s.run("list_tasks", Input{Query: "handoff context", Status: ptr("done")})
	if e != nil || len(v.([]Task)) != 1 {
		t.Fatal("completed comments are not searchable")
	}
}
func TestPersistenceAndExclusiveProcessLock(t *testing.T) {
	dir := t.TempDir()
	s, e := openStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	task := create(t, s)
	if other, e := openStore(dir); e == nil {
		other.close()
		t.Fatal("second writer accepted")
	}
	s.close()
	s, e = openStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	v, e := s.run("get_task", Input{ID: task.ID})
	if e != nil || v.(Task).Title != task.Title {
		t.Fatal("task did not survive restart")
	}
}
func TestFailedSaveDoesNotChangeMemory(t *testing.T) {
	s := testStore(t)
	task := create(t, s)
	s.path = filepath.Join(t.TempDir(), "missing", "board.json")
	if _, e := s.run("claim_task", Input{ID: task.ID, Revision: 1, Actor: "alpha"}); e == nil {
		t.Fatal("save should fail")
	}
	v, e := s.run("get_task", Input{ID: task.ID})
	if e != nil || v.(Task).Revision != 1 || v.(Task).Assignee != "" {
		t.Fatal("failed save changed memory")
	}
}
func TestConcurrentCommentsPersist(t *testing.T) {
	s := testStore(t)
	task := create(t, s)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.run("add_comment", Input{ID: task.ID, Actor: "reviewer", Text: "Important note"}); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	b, e := os.ReadFile(s.path)
	if e != nil {
		t.Fatal(e)
	}
	var saved []Task
	if e = json.Unmarshal(b, &saved); e != nil {
		t.Fatal(e)
	}
	if len(saved[0].Comments) != 20 || saved[0].Revision != 21 {
		t.Fatal("lost concurrent comment")
	}
}
func TestCorruptStoreFailsClosed(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "board.json"), []byte("invalid"), 0600)
	if s, e := openStore(dir); e == nil {
		s.close()
		t.Fatal("corrupt board silently discarded")
	}
}
func TestReleaseAndValidation(t *testing.T) {
	s := testStore(t)
	if _, e := s.run("create_task", Input{Title: ptr(""), Actor: "a"}); e == nil {
		t.Fatal("blank title accepted")
	}
	if _, e := s.run("create_task", Input{Title: ptr("x"), Actor: "a", Status: ptr("in_progress")}); e == nil {
		t.Fatal("ownerless work accepted")
	}
	task := create(t, s)
	v, _ := s.run("claim_task", Input{ID: task.ID, Revision: 1, Actor: "a"})
	task = v.(Task)
	if _, e := s.run("release_task", Input{ID: task.ID, Revision: task.Revision, Actor: "b"}); e == nil {
		t.Fatal("non-owner released")
	}
	v, e := s.run("release_task", Input{ID: task.ID, Revision: task.Revision, Actor: "a"})
	if e != nil {
		t.Fatal(e)
	}
	task = v.(Task)
	if task.Assignee != "" || task.Status != "backlog" {
		t.Fatal("bad release")
	}
}
