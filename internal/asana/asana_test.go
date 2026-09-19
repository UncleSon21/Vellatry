package asana

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient(t *testing.T) {
	var created map[string]map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /workspaces", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"gid":"1","name":"Koala"}]}`))
	})
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("workspace") != "1" || r.URL.Query().Get("archived") != "false" {
			t.Errorf("projects query %s", r.URL.RawQuery)
		}
		w.Write([]byte(`{"data":[{"gid":"10","name":"Marketing"}]}`))
	})
	mux.HandleFunc("GET /tasks/{ext}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("ext") == "external:vellatry:org:fix:7" {
			w.Write([]byte(`{"data":{"gid":"99","permalink_url":"https://app.asana.com/0/10/99","completed":true}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"errors":[{"message":"Not found"}]}`))
	})
	mux.HandleFunc("POST /tasks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&created)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"data":{"gid":"100","permalink_url":"https://app.asana.com/0/10/100","completed":false}}`))
	})
	mux.HandleFunc("PUT /tasks/{gid}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("gid") {
		case "401":
			w.WriteHeader(http.StatusUnauthorized)
		case "429":
			w.WriteHeader(http.StatusTooManyRequests)
		case "403":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"errors":[{"message":"no access to project"}]}`))
		default:
			w.Write([]byte(`{"data":{}}`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	ctx := context.Background()

	ws, err := c.Workspaces(ctx)
	if err != nil || len(ws) != 1 || ws[0].Name != "Koala" {
		t.Fatalf("workspaces = %+v, %v", ws, err)
	}
	ps, err := c.Projects(ctx, "1")
	if err != nil || len(ps) != 1 || ps[0].Workspace != "1" {
		t.Fatalf("projects = %+v, %v", ps, err)
	}
	task, found, err := c.TaskByExternal(ctx, "vellatry:org:fix:7")
	if err != nil || !found || task.GID != "99" || !task.Completed {
		t.Errorf("by external = %+v %v %v", task, found, err)
	}
	if _, found, err := c.TaskByExternal(ctx, "vellatry:org:fix:8"); err != nil || found {
		t.Errorf("missing task: found %v, err %v", found, err)
	}
	task, err = c.CreateTask(ctx, NewTask{Project: "10", Name: "Let GPTBot in", Notes: "robots.txt", ExternalID: "vellatry:org:fix:8"})
	if err != nil || task.GID != "100" {
		t.Fatalf("create = %+v, %v", task, err)
	}
	data := created["data"]
	if data["name"] != "Let GPTBot in" || data["external"].(map[string]any)["gid"] != "vellatry:org:fix:8" || data["projects"].([]any)[0] != "10" {
		t.Errorf("create body = %v", created)
	}
	if err := c.SetCompleted(ctx, "100", true); err != nil {
		t.Error(err)
	}
	if err := c.SetCompleted(ctx, "401", true); !errors.Is(err, ErrAuth) {
		t.Errorf("401: %v", err)
	}
	if err := c.SetCompleted(ctx, "403", true); !errors.Is(err, ErrRejected) {
		t.Errorf("403: %v", err)
	}
	if err := c.SetCompleted(ctx, "429", true); err == nil || errors.Is(err, ErrRejected) || errors.Is(err, ErrAuth) {
		t.Errorf("429 should be transient: %v", err)
	}
}
