package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func project(t *testing.T, s *Store, name string) Project {
	t.Helper()
	v, e := s.runAs("create_project", Input{Name: name}, true)
	if e != nil {
		t.Fatal(e)
	}
	return v.(Project)
}
func queue(t *testing.T, s *Store, p string, limit int) Queue {
	t.Helper()
	v, e := s.runAs("create_queue", Input{ProjectID: p, Name: "PR integration", MaxInFlight: limit}, true)
	if e != nil {
		t.Fatal(e)
	}
	return v.(Queue)
}
func queued(t *testing.T, s *Store, p, q string) Task {
	t.Helper()
	v, e := s.run("create_task", Input{ProjectID: p, QueueID: ptr(q), Title: ptr("Integrate reviewed PR"), Actor: "planner"})
	if e != nil {
		t.Fatal(e)
	}
	return v.(Task)
}
func TestProjectScopeAndQueueReferences(t *testing.T) {
	s := testStore(t)
	a := project(t, s, "A")
	b := project(t, s, "B")
	q := queue(t, s, a.ID, 1)
	task := queued(t, s, a.ID, q.ID)
	for _, op := range []string{"get_task", "claim_task", "update_task", "release_task", "add_comment"} {
		if _, e := s.runAs(op, Input{ProjectID: b.ID, ID: task.ID, Actor: "admin", Revision: 1, Text: "note"}, true); e == nil {
			t.Fatalf("%s crossed project", op)
		}
	}
	v, e := s.run("list_tasks", Input{ProjectID: b.ID})
	if e != nil || len(v.([]Task)) != 0 {
		t.Fatal("project leak")
	}
	if _, e = s.run("create_task", Input{ProjectID: b.ID, QueueID: ptr(q.ID), Title: ptr("bad"), Actor: "a"}); e == nil {
		t.Fatal("cross-project queue accepted")
	}
	if _, e = s.run("list_tasks", Input{ProjectID: "missing"}); e == nil {
		t.Fatal("missing project silently accepted")
	}
	if _, e = s.run("create_project", Input{Name: "No permission"}); e == nil {
		t.Fatal("member created project")
	}
	if _, e = s.run("create_queue", Input{Name: "No permission"}); e == nil {
		t.Fatal("member created queue")
	}
}
func TestConcurrentSupervisorClaimsAndRecovery(t *testing.T) {
	s := testStore(t)
	q := queue(t, s, "default", 1)
	first := queued(t, s, "default", q.ID)
	second := queued(t, s, "default", q.ID)
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := []Task{}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, e := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: fmt.Sprintf("worker-%d", i)})
			if e != nil {
				t.Error(e)
			}
			if v != nil {
				mu.Lock()
				claimed = append(claimed, v.(Task))
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(claimed) != 1 || claimed[0].ID != first.ID {
		t.Fatalf("not one FIFO claim: %+v", claimed)
	}
	task := claimed[0]
	v, e := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: task.Assignee})
	if e != nil || v.(Task).ID != first.ID || v.(Task).Revision != task.Revision {
		t.Fatal("retry did not recover existing claim")
	}
	if _, e = s.run("claim_task", Input{ID: second.ID, Actor: "other", Revision: 1}); e == nil {
		t.Fatal("manual claim bypassed queue limit")
	}
	if _, e = s.runAs("update_task", Input{ID: second.ID, Actor: "admin", Revision: 1, Assignee: ptr("other"), Status: ptr("in_progress")}, true); e == nil {
		t.Fatal("admin bypassed queue limit")
	}
	if _, e = s.run("update_task", Input{ID: task.ID, Actor: task.Assignee, Revision: task.Revision, Status: ptr("done")}); e != nil {
		t.Fatal(e)
	}
	v, e = s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "next"})
	if e != nil || v.(Task).ID != second.ID {
		t.Fatal("next FIFO claim failed")
	}
}
func TestAdminOverridesStillRequireRevisionAndRecordHistory(t *testing.T) {
	s := testStore(t)
	task := create(t, s)
	v, _ := s.run("claim_task", Input{ID: task.ID, Actor: "worker", Revision: 1})
	task = v.(Task)
	if _, e := s.run("update_task", Input{ID: task.ID, Actor: "human", Revision: task.Revision, Title: ptr("edited")}); e == nil {
		t.Fatal("member override accepted")
	}
	if _, e := s.runAs("update_task", Input{ID: task.ID, Actor: "human", Revision: 1, Title: ptr("edited")}, true); e == nil {
		t.Fatal("admin stale edit accepted")
	}
	v, e := s.runAs("update_task", Input{ID: task.ID, Actor: "human", Revision: task.Revision, Title: ptr("edited"), Assignee: ptr("replacement")}, true)
	if e != nil {
		t.Fatal(e)
	}
	task = v.(Task)
	if task.Title != "edited" || task.Assignee != "replacement" || task.History[len(task.History)-1].Text != "Administrator action: update_task" {
		t.Fatal("admin change not audited")
	}
	if _, e = s.runAs("release_task", Input{ID: task.ID, Actor: "human", Revision: task.Revision}, true); e != nil {
		t.Fatal(e)
	}
}
func TestLegacyMigrationAndRestart(t *testing.T) {
	dir := t.TempDir()
	original := []byte(`[{"id":"old","title":"Old ticket","assignee":"","description":"keep","status":"backlog","revision":7,"comments":[{"author":"a","text":"memory","at":"then"}],"history":[]}]`)
	if e := os.WriteFile(filepath.Join(dir, "board.json"), original, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := openStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.run("get_task", Input{ID: "old"})
	if e != nil || v.(Task).ProjectID != "default" || v.(Task).Revision != 7 {
		t.Fatal("legacy task lost")
	}
	p := project(t, s, "Deployments")
	q := queue(t, s, p.ID, 2)
	queued(t, s, p.ID, q.ID)
	s.close()
	backup, e := os.ReadFile(filepath.Join(dir, "board.json.v1.bak"))
	if e != nil || string(backup) != string(original) {
		t.Fatal("legacy backup differs")
	}
	s, e = openStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	v, e = s.run("get_task", Input{ID: "old"})
	if e != nil || v.(Task).Comments[0].Text != "memory" {
		t.Fatal("legacy memory lost")
	}
	v, e = s.run("list_queues", Input{ProjectID: p.ID})
	if e != nil || len(v.([]Queue)) != 1 {
		t.Fatal("queue lost on restart")
	}
	var state State
	b, _ := os.ReadFile(s.path)
	if e = json.Unmarshal(b, &state); e != nil || state.Version != 2 {
		t.Fatal("v2 not persisted")
	}
}
func TestMetadataSaveFailureDoesNotMutate(t *testing.T) {
	s := testStore(t)
	s.path = filepath.Join(t.TempDir(), "missing", "board.json")
	if _, e := s.runAs("create_project", Input{Name: "bad"}, true); e == nil {
		t.Fatal("expected failed save")
	}
	if len(s.projects) != 1 {
		t.Fatal("failed project leaked into memory")
	}
	if _, e := s.runAs("create_queue", Input{Name: "bad"}, true); e == nil {
		t.Fatal("expected failed save")
	}
	if len(s.queues) != 0 {
		t.Fatal("failed queue leaked into memory")
	}
}
func TestQueueReleaseGoesToTailAndBlockedDoesNotRun(t *testing.T) {
	s := testStore(t)
	q := queue(t, s, "default", 1)
	a := queued(t, s, "default", q.ID)
	b := queued(t, s, "default", q.ID)
	v, _ := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "w"})
	task := v.(Task)
	if task.ID != a.ID {
		t.Fatal("FIFO")
	}
	s.run("release_task", Input{ID: task.ID, Actor: "w", Revision: task.Revision})
	v, e := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "w"})
	if e != nil || v.(Task).ID != b.ID {
		t.Fatal("released ticket did not move to tail")
	}
	task = v.(Task)
	s.run("update_task", Input{ID: task.ID, Actor: "w", Revision: task.Revision, Status: ptr("blocked")})
	v, e = s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "w"})
	if e != nil || v.(Task).ID != a.ID {
		t.Fatal("blocked ticket selected")
	}
}
func TestAdminAuthenticationRESTAndMCP(t *testing.T) {
	s := testStore(t)
	task := create(t, s)
	s.run("claim_task", Input{ID: task.ID, Actor: "worker", Revision: 1})
	h := (&Server{store: s, token: "member-secret", adminToken: "admin-secret", mcp: true}).handler()
	body := fmt.Sprintf(`{"id":%q,"actor":"human","revision":2,"title":"Admin edit"}`, task.ID)
	if w := request(h, "POST", "/api/actions/update_task", body, "member-secret", ""); w.Code != 409 {
		t.Fatalf("member override %d", w.Code)
	}
	if w := request(h, "POST", "/api/actions/create_project", `{"name":"P"}`, "member-secret", ""); w.Code != 403 {
		t.Fatalf("member created project %d", w.Code)
	}
	if w := request(h, "POST", "/api/actions/update_task", body, "admin-secret", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := request(h, "POST", "/api/actions/update_task", `{"id":"x","admin":true}`, "member-secret", ""); w.Code != 400 {
		t.Fatal("client admin field accepted")
	}
	for _, token := range []string{"member-secret", "admin-secret"} {
		w := request(h, "GET", "/api/config", "", token, "")
		var config map[string]any
		json.Unmarshal(w.Body.Bytes(), &config)
		if config["admin"] != (token == "admin-secret") {
			t.Fatal("incorrect role")
		}
	}
	rpc := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_queue","arguments":{"name":"Sync"}}}`
	for _, token := range []string{"member-secret", "admin-secret"} {
		w := request(h, "POST", "/mcp", rpc, token, "")
		var result struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		if result.Result.IsError != (token == "member-secret") {
			t.Fatal("MCP role not enforced")
		}
	}
}
