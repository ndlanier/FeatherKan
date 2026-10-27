package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func request(h http.Handler, method, path, body, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestAuthOriginAndMethods(t *testing.T) {
	h := (&Server{store: testStore(t), token: "secret", mcp: true}).handler()
	for _, p := range []string{"/api/tasks", "/mcp"} {
		if r := request(h, "GET", p, "", "", ""); r.Code != 401 {
			t.Fatalf("unauthorized %s: %d", p, r.Code)
		}
	}
	if r := request(h, "GET", "/api/tasks", "", "secret", "https://evil.example"); r.Code != 403 {
		t.Fatal("cross-origin accepted")
	}
	if r := request(h, "GET", "/api/tasks", "", "secret", "http://localhost"); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r := request(h, "GET", "/mcp", "", "secret", ""); r.Code != 405 {
		t.Fatal("MCP GET should be 405")
	}
	if r := request(h, "POST", "/api/actions/create_task", `{"title":"ok","actor":"a"} {}`, "secret", ""); r.Code != 400 {
		t.Fatal("trailing JSON accepted")
	}
}
func TestMCPWorkflow(t *testing.T) {
	h := (&Server{store: testStore(t), mcp: true}).handler()
	call := func(body string) map[string]any {
		t.Helper()
		r := request(h, "POST", "/mcp", body, "", "")
		if r.Code != 200 {
			t.Fatalf("MCP HTTP %d: %s", r.Code, r.Body.String())
		}
		var v map[string]any
		if e := json.Unmarshal(r.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	v := call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if v["result"].(map[string]any)["protocolVersion"] != "2025-11-25" {
		t.Fatal(v)
	}
	if r := request(h, "POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "", ""); r.Code != 202 {
		t.Fatal("notification not accepted")
	}
	v = call(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if len(v["result"].(map[string]any)["tools"].([]any)) != 7 {
		t.Fatal("missing tools")
	}
	v = call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_task","arguments":{"title":"MCP task","actor":"agent"}}}`)
	result := v["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatal(v)
	}
	v = call(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"claim_task","arguments":{"id":"missing","revision":1,"actor":"agent"}}}`)
	if v["result"].(map[string]any)["isError"] != true {
		t.Fatal("tool failure not reported")
	}
	if r := request(h, "GET", "/api/tasks", "", "", ""); !strings.Contains(r.Body.String(), "MCP task") {
		t.Fatal("MCP and UI do not share store")
	}
}
func TestMCPDisabled(t *testing.T) {
	h := (&Server{store: testStore(t)}).handler()
	if r := request(h, "POST", "/mcp", `{}`, "", ""); r.Code != 404 {
		t.Fatal("MCP enabled by default")
	}
}
func TestUntrustedHostWithoutToken(t *testing.T) {
	h := (&Server{store: testStore(t)}).handler()
	r := httptest.NewRequest("GET", "http://untrusted.example/api/tasks", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("untrusted Host allowed")
	}
}
