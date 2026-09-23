// Package embed turns short texts into vectors with Vellatry's own embedding service
// (ml/embed) and compares them, so the product can tell when two topics mean the same
// thing or which topic a search query belongs to.
//
// Inference only: the model is pretrained and pinned, and nothing here learns from
// customer data. Vectors are comparable only with others from the same model, so the
// model's name travels with every vector and Similar refuses to mix them.
//
// What a vector may decide is deliberately narrow. A similarity is a suggestion for a
// person ("these look like the same topic"), never a number shown as a measurement:
// scores in reports stay deterministic.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Vector is one text's embedding. The service returns unit vectors.
type Vector []float32

// Result is a batch of vectors and the model that made them.
type Result struct {
	Model   string
	Dim     int
	Vectors []Vector
}

// Client calls the embedding service. It is the worker's; the api never embeds.
type Client struct {
	URL   string       // http://vellatry-embed.flycast in production
	HTTP  *http.Client // optional
	Batch int          // texts per request; the service caps at 256
}

// MaxBatch is the service's own limit.
const MaxBatch = 256

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *Client) batch() int {
	switch {
	case c.Batch <= 0:
		return 128
	case c.Batch > MaxBatch:
		return MaxBatch
	}
	return c.Batch
}

// Embed returns one vector per text, in order. An empty text is refused rather than
// embedded: an empty vector would silently match everything.
func (c *Client) Embed(ctx context.Context, texts []string) (Result, error) {
	var out Result
	if len(texts) == 0 {
		return out, nil
	}
	for i, t := range texts {
		if strings.TrimSpace(t) == "" {
			return out, fmt.Errorf("embed: text %d is empty", i)
		}
	}
	for start := 0; start < len(texts); start += c.batch() {
		end := min(start+c.batch(), len(texts))
		part, err := c.one(ctx, texts[start:end])
		if err != nil {
			return Result{}, err
		}
		if out.Model == "" {
			out.Model, out.Dim = part.Model, part.Dim
		} else if part.Model != out.Model || part.Dim != out.Dim {
			// The service was redeployed with another model mid-run. Half a corpus in
			// each model compares as noise, so fail and let the job start again.
			return Result{}, fmt.Errorf("embed: model changed mid-run (%s/%d then %s/%d)", out.Model, out.Dim, part.Model, part.Dim)
		}
		out.Vectors = append(out.Vectors, part.Vectors...)
	}
	return out, nil
}

func (c *Client) one(ctx context.Context, texts []string) (Result, error) {
	body, err := json.Marshal(map[string]any{"texts": texts})
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/embed", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("embed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Result{}, fmt.Errorf("embed: %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	var payload struct {
		Model   string   `json:"model"`
		Dim     int      `json:"dim"`
		Vectors []Vector `json:"vectors"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&payload); err != nil {
		return Result{}, fmt.Errorf("embed: bad response: %w", err)
	}
	if payload.Model == "" || payload.Dim <= 0 {
		return Result{}, fmt.Errorf("embed: response names no model")
	}
	if len(payload.Vectors) != len(texts) {
		return Result{}, fmt.Errorf("embed: asked for %d vectors, got %d", len(texts), len(payload.Vectors))
	}
	for i, v := range payload.Vectors {
		if len(v) != payload.Dim {
			return Result{}, fmt.Errorf("embed: vector %d has %d dimensions, not %d", i, len(v), payload.Dim)
		}
	}
	return Result{Model: payload.Model, Dim: payload.Dim, Vectors: payload.Vectors}, nil
}
