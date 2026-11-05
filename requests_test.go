package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestPayloadScopePersistenceAndFailure(t *testing.T) {
	dir := t.TempDir()
	s, e := openStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	p := project(t, s, "Other")
	q := queue(t, s, "default", 2)
	for _, payload := range []string{`{"n":9007199254740993,"nested":{"items":[true,null,"x"]}}`, `[1,2]`, `"message"`, `null`} {
		v, e := s.run("enqueue_request", Input{QueueID: ptr(q.ID), Actor: "sender", Payload: json.RawMessage(payload)})
		if e != nil {
			t.Fatal(e)
		}
		r := v.(Request)
		if string(r.Payload) != payload {
			t.Fatal("payload changed")
		}
		if _, e = s.run("get_request", Input{ProjectID: p.ID, QueueID: ptr(q.ID), ID: r.ID}); e == nil {
			t.Fatal("cross-project access")
		}
		if _, e = s.run("get_task", Input{ID: r.ID}); e == nil {
			t.Fatal("request exposed as ticket")
		}
	}
	if _, e = s.run("enqueue_request", Input{QueueID: ptr(q.ID), Actor: "sender"}); e == nil {
		t.Fatal("missing payload accepted")
	}
	if _, e = s.run("enqueue_request", Input{QueueID: ptr(q.ID), Actor: "sender", Payload: json.RawMessage(`"` + strings.Repeat("x", 30001) + `"`)}); e == nil {
		t.Fatal("oversized payload")
	}
	a, _ := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "a"})
	b, _ := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "b"})
	if a == nil || b == nil {
		t.Fatal("capacity 2 not honored")
	}
	c, _ := s.run("claim_next", Input{QueueID: ptr(q.ID), Actor: "c"})
	if c != nil {
		t.Fatal("capacity exceeded")
	}
	originalPath := s.path
	s.path = filepath.Join(dir, "missing", "board.json")
	r := a.(Request)
	if _, e = s.run("complete_request", Input{QueueID: ptr(q.ID), ID: r.ID, Actor: "a", Revision: r.Revision}); e == nil {
		t.Fatal("failed write succeeded")
	}
	if s.requests[0].Status != "active" {
		t.Fatal("failed write mutated state")
	}
	s.path = originalPath
	s.close()
	s, e = openStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	if len(s.requests) != 4 || s.requests[0].Status != "active" || len(s.tasks) != 0 {
		t.Fatal("restart lost requests")
	}
	if !strings.Contains(string(s.requests[0].Payload), "9007199254740993") {
		t.Fatal("JSON number lost precision")
	}
}
func TestV2QueueMigrationPreservesTickets(t *testing.T) {
	dir := t.TempDir()
	original := []byte(`{"version":2,"projects":[{"id":"default","name":"Default"}],"queues":[{"id":"q","projectId":"default","name":"Merge","maxInFlight":1}],"tasks":[{"id":"t","projectId":"default","queueId":"q","title":"Keep me","status":"in_progress","assignee":"worker","revision":2,"comments":[],"history":[]}]}`)
	if e := os.WriteFile(filepath.Join(dir, "board.json"), original, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := openStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	if len(s.tasks) != 1 || s.tasks[0].QueueID != "" || s.tasks[0].Title != "Keep me" || s.tasks[0].Status != "in_progress" || len(s.requests) != 0 {
		t.Fatal("migration changed work or invented requests")
	}
	backup, e := os.ReadFile(filepath.Join(dir, "board.json.v2.bak"))
	if e != nil || string(backup) != string(original) {
		t.Fatal("backup differs")
	}
	if _, e = s.run("claim_next", Input{QueueID: ptr("q"), Actor: "supervisor"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.run("create_task", Input{Title: ptr("No routing"), QueueID: ptr("q"), Actor: "a"}); e == nil {
		t.Fatal("ticket routing still accepted")
	}
}
func TestRequestRESTAndMCP(t *testing.T) {
	s := testStore(t)
	q := queue(t, s, "default", 1)
	h := (&Server{store: s, token: "member", mcp: true}).handler()
	body := `{"queueId":"` + q.ID + `","actor":"sender","payload":{"type":"sync","application":"api"}}`
	w := request(h, "POST", "/api/actions/enqueue_request", body, "member", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(h, "GET", "/api/requests?queueId="+q.ID, "", "member", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"payload"`) {
		t.Fatal(w.Body.String())
	}
	rpc := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"claim_next","arguments":{"queueId":"` + q.ID + `","actor":"supervisor"}}}`
	w = request(h, "POST", "/mcp", rpc, "member", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `\"claimedBy\":\"supervisor\"`) {
		t.Fatal(w.Body.String())
	}
}
