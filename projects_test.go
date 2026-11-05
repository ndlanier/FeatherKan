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
func queued(t *testing.T, s *Store, p, q string) Request {
	t.Helper()
	v, e := s.run("enqueue_request", Input{ProjectID: p, QueueID: ptr(q), Payload: json.RawMessage(`{"type":"merge","prs":[17,18]}`), Actor: "planner"})
	if e != nil {
		t.Fatal(e)
	}
	return v.(Request)
}
func TestProjectScopeAndQueueReferences(t *testing.T) {
	s := testStore(t)
	a := project(t, s, "A")
	b := project(t, s, "B")
	q := queue(t, s, a.ID, 1)
	v0, e0 := s.run("create_task", Input{ProjectID: a.ID, Title: ptr("Task"), Actor: "planner"})
	if e0 != nil {
		t.Fatal(e0)
	}
	task := v0.(Task)
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
	claimed := []Request{}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, e := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: fmt.Sprintf("w-%d", i)})
			if e != nil {
				t.Error(e)
			}
			if v != nil {
				mu.Lock()
				claimed = append(claimed, v.(Request))
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(claimed) != 1 || claimed[0].ID != first.ID {
		t.Fatalf("not one FIFO claim: %+v", claimed)
	}
	r := claimed[0]
	v, e := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: r.ClaimedBy})
	if e != nil || v.(Request).Revision != r.Revision {
		t.Fatal("retry changed claim")
	}
	if len(s.tasks) != 0 {
		t.Fatal("requests appeared as tickets")
	}
	if _, e = s.run("complete_request", Input{QueueID: ptr(q.ID), ID: r.ID, Actor: "other", Revision: r.Revision}); e == nil {
		t.Fatal("non-owner completed")
	}
	if _, e = s.runAs("complete_request", Input{QueueID: ptr(q.ID), ID: r.ID, Actor: "admin", Revision: 1}, true); e == nil {
		t.Fatal("admin bypassed revision")
	}
	if _, e = s.run("complete_request", Input{QueueID: ptr(q.ID), ID: r.ID, Actor: r.ClaimedBy, Revision: r.Revision, Result: json.RawMessage(`{"sha":"abc"}`)}); e != nil {
		t.Fatal(e)
	}
	v, e = s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "next"})
	if e != nil || v.(Request).ID != second.ID {
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
	if e = json.Unmarshal(b, &state); e != nil || state.Version != 3 {
		t.Fatal("v3 not persisted")
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
func TestQueueReleaseAndFailure(t *testing.T) {
	s := testStore(t)
	q := queue(t, s, "default", 1)
	a := queued(t, s, "default", q.ID)
	b := queued(t, s, "default", q.ID)
	v, _ := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "w"})
	r := v.(Request)
	if _, e := s.run("release_request", Input{QueueID: ptr(q.ID), ID: r.ID, Actor: "w", Revision: r.Revision}); e != nil {
		t.Fatal(e)
	}
	v, e := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "w"})
	if e != nil || v.(Request).ID != b.ID {
		t.Fatal("release did not move to tail")
	}
	r = v.(Request)
	if _, e = s.run("fail_request", Input{QueueID: ptr(q.ID), ID: r.ID, Actor: "w", Revision: r.Revision, Text: "Checks failed"}); e != nil {
		t.Fatal(e)
	}
	v, e = s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "w"})
	if e != nil || v.(Request).ID != a.ID {
		t.Fatal("failed request selected")
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

func TestQueueFIFOWithVariableTimestampPrecision(t *testing.T) {
	s := testStore(t)
	q := queue(t, s, "default", 1)
	first := queued(t, s, "default", q.ID)
	queued(t, s, "default", q.ID)
	s.requests[0].EnqueuedAt = "2026-11-04T12:00:00.1Z"
	s.requests[1].EnqueuedAt = "2026-11-04T12:00:00.11Z"
	v, err := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if v.(Request).ID != first.ID {
		t.Fatal("FIFO used timestamp text instead of chronological order")
	}
}
