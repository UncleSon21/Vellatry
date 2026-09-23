package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// service is a stand-in for ml/embed: it answers with one vector per text, built from
// the text so the same text always embeds the same way.
func service(t *testing.T, model string, dim int) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		vectors := make([][]float32, len(in.Texts))
		for i, text := range in.Texts {
			v := make([]float32, dim)
			for j := range v {
				v[j] = float32(len(text)+j) / 10
			}
			vectors[i] = v
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "dim": dim, "vectors": vectors})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestEmbedBatches(t *testing.T) {
	srv, calls := service(t, "bge-small", 4)
	c := &Client{URL: srv.URL, Batch: 2}
	texts := []string{"mattress", "sofa beds", "bed bases", "pillows", "quilts"}

	got, err := c.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "bge-small" || got.Dim != 4 {
		t.Errorf("model %s, dim %d", got.Model, got.Dim)
	}
	if len(got.Vectors) != len(texts) {
		t.Fatalf("%d vectors for %d texts", len(got.Vectors), len(texts))
	}
	if *calls != 3 { // 2 + 2 + 1
		t.Errorf("%d requests, want 3 for a batch size of 2", *calls)
	}
	// Order is preserved across batches: the fifth vector belongs to the fifth text.
	if got.Vectors[4][0] != float32(len("quilts"))/10 {
		t.Errorf("vectors came back out of order: %v", got.Vectors[4])
	}
}

func TestEmbedRefusesEmptyText(t *testing.T) {
	srv, calls := service(t, "bge-small", 4)
	c := &Client{URL: srv.URL}
	if _, err := c.Embed(context.Background(), []string{"mattress", "  "}); err == nil {
		t.Error("an empty text was embedded; an empty vector matches everything")
	}
	if *calls != 0 {
		t.Errorf("the service was called anyway (%d times)", *calls)
	}
}

func TestEmbedRefusesAChangedModel(t *testing.T) {
	// The service is redeployed with another model between batches.
	first := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		model := "bge-small"
		if !first {
			model = "something-else"
		}
		first = false
		var in struct {
			Texts []string `json:"texts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		vectors := make([][]float32, len(in.Texts))
		for i := range vectors {
			vectors[i] = []float32{1, 0}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "dim": 2, "vectors": vectors})
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, Batch: 1}
	_, err := c.Embed(context.Background(), []string{"one", "two"})
	if err == nil || !strings.Contains(err.Error(), "model changed") {
		t.Errorf("a run across two models was accepted: %v", err)
	}
}

func TestEmbedRejectsABadResponse(t *testing.T) {
	cases := map[string]any{
		"no model":        map[string]any{"dim": 2, "vectors": [][]float32{{1, 0}}},
		"missing vectors": map[string]any{"model": "m", "dim": 2, "vectors": [][]float32{}},
		"wrong width":     map[string]any{"model": "m", "dim": 3, "vectors": [][]float32{{1, 0}}},
	}
	for name, payload := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(payload)
		}))
		c := &Client{URL: srv.URL}
		if _, err := c.Embed(context.Background(), []string{"mattress"}); err == nil {
			t.Errorf("%s was accepted", name)
		}
		srv.Close()
	}
}

func TestEmbedReportsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model is loading", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := (&Client{URL: srv.URL}).Embed(context.Background(), []string{"mattress"})
	if err == nil || !strings.Contains(err.Error(), "model is loading") {
		t.Errorf("error = %v; it should carry what the service said", err)
	}
}

func TestCosineAndSimilar(t *testing.T) {
	same := Vector{1, 0, 0}
	other := Vector{0, 1, 0}
	if s := Cosine(same, same); s < 0.999 {
		t.Errorf("a vector against itself = %v", s)
	}
	if s := Cosine(same, other); s > 0.001 {
		t.Errorf("unrelated vectors = %v", s)
	}
	// Length must not matter: only direction.
	if s := Cosine(Vector{2, 0, 0}, Vector{0.5, 0, 0}); s < 0.999 {
		t.Errorf("same direction, different length = %v", s)
	}
	if s := Cosine(Vector{1, 0}, Vector{1, 0, 0}); s != 0 {
		t.Errorf("different widths = %v, want 0", s)
	}

	candidates := []Candidate{
		{ID: "mattresses", Model: "m", Vector: Vector{0.9, 0.1, 0}},
		{ID: "sofa beds", Model: "m", Vector: Vector{0, 1, 0}},
		{ID: "beds", Model: "m", Vector: Vector{0.8, 0.2, 0}},
		{ID: "from another model", Model: "older", Vector: Vector{1, 0, 0}},
	}
	got := Similar("m", Vector{1, 0, 0}, candidates, 0.8, 5)
	if len(got) != 2 || got[0].ID != "mattresses" || got[1].ID != "beds" {
		t.Fatalf("matches = %+v; best first, and only this model's", got)
	}
	if limited := Similar("m", Vector{1, 0, 0}, candidates, 0.8, 1); len(limited) != 1 {
		t.Errorf("limit ignored: %+v", limited)
	}
	if none := Similar("m", Vector{0, 0, 1}, candidates, 0.8, 5); len(none) != 0 {
		t.Errorf("nothing should match: %+v", none)
	}
}
