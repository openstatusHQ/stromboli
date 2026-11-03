// Package flytest is a fake Fly.io Machines API for tests.
package flytest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Token is the token the fake accepts (sent as "FlyV1 fm2_deploy").
const Token = "fm2_deploy"

// Server fakes one app's machines: "148e" and "2a7c" in ams, and "d91v",
// which mounts a volume, in fra.
type Server struct {
	*httptest.Server
	App string

	mu    sync.Mutex
	calls []string
	// Created holds the bodies of machine creations (clones).
	Created []map[string]any
	// Fail makes calls whose "METHOD path" contains it return 500.
	Fail string
}

const machinesJSON = `[
 {"id":"148e","name":"api-1","state":"started","region":"ams","instance_id":"01A","updated_at":"2026-11-03T10:00:00Z",
  "image_ref":{"registry":"registry.fly.io","repository":"api","tag":"deployment-01"},
  "config":{"image":"registry.fly.io/api:deployment-01","guest":{"cpu_kind":"shared","cpus":1,"memory_mb":512},"metadata":{"fly_process_group":"app"},"env":{"PORT":"8080"}}},
 {"id":"2a7c","name":"api-2","state":"started","region":"ams","updated_at":"2026-11-03T10:00:00Z",
  "image_ref":{"registry":"registry.fly.io","repository":"api","tag":"deployment-01"},
  "config":{"image":"registry.fly.io/api:deployment-01","guest":{"cpu_kind":"shared","cpus":1,"memory_mb":512},"metadata":{"fly_process_group":"app"}}},
 {"id":"d91v","name":"api-db","state":"started","region":"fra","updated_at":"2026-11-03T10:00:00Z",
  "config":{"image":"postgres:17","mounts":[{"volume":"vol_123","path":"/data"}]}}
]`

// New starts a fake for app.
func New(app string) *Server {
	s := &Server{App: app}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Calls returns the mutating calls received, e.g. "POST 148e/restart".
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "FlyV1 "+Token {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
		return
	}
	if s.Fail != "" && strings.Contains(r.Method+" "+r.URL.Path, s.Fail) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"boom"}`)
		return
	}
	if r.URL.Path == "/v1/apps" {
		fmt.Fprint(w, `{"total_apps":1,"apps":[]}`)
		return
	}
	prefix := "/v1/apps/" + s.App + "/machines"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"app not found"}`)
		return
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")
	var machines []map[string]any
	json.Unmarshal([]byte(machinesJSON), &machines)
	find := func(id string) map[string]any {
		for _, m := range machines {
			if m["id"] == id {
				return m
			}
		}
		return nil
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		fmt.Fprint(w, machinesJSON)
	case rest == "" && r.Method == http.MethodPost:
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.Created = append(s.Created, body)
		n := len(s.Created)
		s.calls = append(s.calls, "POST create")
		s.mu.Unlock()
		fmt.Fprintf(w, `{"id":"new%d","state":"created","region":%q,"config":%s}`, n, body["region"], mustJSON(body["config"]))
	default:
		id, op, _ := strings.Cut(rest, "/")
		if op == "wait" && strings.HasPrefix(id, "new") {
			fmt.Fprint(w, `{"ok":true}`) // a clone created by this fake
			return
		}
		m := find(id)
		if m == nil {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"machine not found"}`)
			return
		}
		switch {
		case op == "" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(m)
		case op == "wait":
			fmt.Fprint(w, `{"ok":true}`)
		case r.Method == http.MethodPost:
			s.mu.Lock()
			s.calls = append(s.calls, "POST "+id+"/"+op)
			s.mu.Unlock()
			fmt.Fprint(w, `{"ok":true}`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
