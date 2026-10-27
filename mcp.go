package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

func toolList() []map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	properties := map[string]any{"id": str("Task ID"), "actor": str("Your stable agent name; use the same name for claims, updates and handoffs"), "revision": map[string]any{"type": "integer", "description": "Latest task revision from get_task; required for claims, updates and release"}, "title": str("Task title, max 200 characters"), "description": str("Scope, affected files, acceptance criteria, decisions and handoff context"), "assignee": str("Agent name; empty string means unassigned"), "status": map[string]any{"type": "string", "enum": []string{"backlog", "in_progress", "blocked", "done"}}, "text": str("Comment or handoff note, max 10000 characters"), "query": str("Search task fields, comments and history")}
	defs := []struct {
		name, description string
		fields, required  []string
	}{
		{"list_tasks", "Read shared memory before starting work. Search current and completed work to detect overlapping scope.", []string{"query", "status", "assignee"}, []string{}},
		{"get_task", "Read the complete task, comments, history and latest revision.", []string{"id"}, []string{"id"}},
		{"create_task", "Create scoped work. Check existing tasks first to avoid duplicating work.", []string{"title", "description", "assignee", "status", "actor"}, []string{"title", "actor"}},
		{"claim_task", "Atomically claim an unassigned task (or start your own assigned task). Do not begin work if the claim fails.", []string{"id", "revision", "actor"}, []string{"id", "revision", "actor"}},
		{"update_task", "Update a task owned by you or an unassigned task. Stale revisions and other owners are rejected. Re-read and reconcile conflicts; never blindly retry. Use assignee for an explicit handoff.", []string{"id", "revision", "actor", "title", "description", "assignee", "status"}, []string{"id", "revision", "actor"}},
		{"add_comment", "Append progress, decisions, blockers, touched files or handoff context. Comments from other agents are welcome.", []string{"id", "actor", "text"}, []string{"id", "actor", "text"}},
		{"release_task", "Release your claimed task back to backlog. Leave a handoff comment first. Claims do not expire automatically.", []string{"id", "revision", "actor"}, []string{"id", "revision", "actor"}},
	}
	out := []map[string]any{}
	for _, d := range defs {
		p := map[string]any{}
		for _, k := range d.fields {
			p[k] = properties[k]
		}
		out = append(out, map[string]any{"name": d.name, "description": d.description, "inputSchema": map[string]any{"type": "object", "properties": p, "required": d.required, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": d.name == "list_tasks" || d.name == "get_task", "destructiveHint": false, "openWorldHint": false}})
	}
	return out
}
func supported(v string) bool { return v == "2025-11-25" || v == "2025-06-18" || v == "2025-03-26" }
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !s.mcp {
		http.NotFound(w, r)
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		http.Error(w, "This stateless MCP server accepts POST only", 405)
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !supported(v) {
		http.Error(w, "Unsupported MCP protocol version", 400)
		return
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	rpcError := func(id json.RawMessage, code int, msg string) {
		if len(id) == 0 {
			id = json.RawMessage("null")
		}
		jsonReply(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	}
	if decode(w, r, &req) != nil {
		rpcError(nil, -32700, "Parse error")
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		rpcError(req.ID, -32600, "Invalid request")
		return
	}
	if len(req.ID) == 0 {
		if strings.HasPrefix(req.Method, "notifications/") {
			w.WriteHeader(202)
		} else {
			http.Error(w, "Invalid notification", 400)
		}
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			rpcError(req.ID, -32602, "Invalid initialize params")
			return
		}
		v := p.ProtocolVersion
		if !supported(v) {
			v = "2025-11-25"
		}
		result = map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "featherkan", "version": "1.0.0"}, "instructions": "Read the board before starting. Use a stable actor name. Claim a task before work and stop if claiming fails. Record affected files and handoffs in comments. Re-read after revision conflicts. Completed tasks remain searchable memory. Ownership is cooperative, not an identity security boundary."}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": toolList()}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			rpcError(req.ID, -32602, "Invalid tool parameters")
			return
		}
		known := false
		for _, t := range toolList() {
			if t["name"] == p.Name {
				known = true
			}
		}
		if !known {
			rpcError(req.ID, -32602, "Unknown tool")
			return
		}
		var in Input
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage("{}")
		}
		d := json.NewDecoder(strings.NewReader(string(p.Arguments)))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil {
			rpcError(req.ID, -32602, "Invalid tool arguments")
			return
		}
		value, err := s.store.run(p.Name, in)
		isError := err != nil
		var text string
		if err != nil {
			if a, ok := err.(*apiError); ok {
				text = a.Message
			} else {
				text = "Storage operation failed"
			}
		} else {
			b, _ := json.Marshal(value)
			text = string(b)
		}
		result = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": isError}
	default:
		rpcError(req.ID, -32601, "Method not found")
		return
	}
	jsonReply(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}
