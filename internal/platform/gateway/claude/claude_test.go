package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/UncleSon21/vellatry/internal/platform/gateway"
)

func fakeAPI(t *testing.T, stopReason string, capture *map[string]any, header *http.Header) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Errorf("path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if capture != nil {
			_ = json.Unmarshal(b, capture)
		}
		if header != nil {
			*header = r.Header.Clone()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-haiku-4-5",
			"content": [{"type": "text", "text": "{\"ok\":true}"}],
			"stop_reason": "` + stopReason + `", "stop_sequence": null,
			"usage": {"input_tokens": 1000, "output_tokens": 200, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 2000}
		}`))
	}))
	t.Cleanup(srv.Close)
	return New("test-key", nil, option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
}

func TestCompleteSendsSchemaAndComputesCost(t *testing.T) {
	var body map[string]any
	p := fakeAPI(t, "end_turn", &body, nil)
	schema := map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}, "additionalProperties": false}
	resp, err := p.Complete(context.Background(), "claude-haiku-4-5", 500, gateway.Request{
		System: "rubric", Schema: schema,
		Messages: []gateway.Message{{Role: "user", Content: "judge this"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"ok":true}` || resp.Model != "claude-haiku-4-5" {
		t.Errorf("resp = %+v", resp)
	}
	// (1000 x $1 + 2000 x $0.10 + 200 x $5) / 1M = 0.0022
	if math.Abs(resp.Usage.CostUSD-0.0022) > 1e-9 {
		t.Errorf("cost = %v, want 0.0022", resp.Usage.CostUSD)
	}
	if resp.Usage.InputTokens != 3000 || resp.Usage.OutputTokens != 200 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	format := body["output_config"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil {
		t.Errorf("output_config = %v", body["output_config"])
	}
	system := body["system"].([]any)[0].(map[string]any)
	if system["cache_control"] == nil {
		t.Errorf("system prompt not cached: %v", system)
	}
	if _, ok := body["fallbacks"]; ok {
		t.Errorf("haiku request should not carry fallbacks")
	}
	if _, ok := body["temperature"]; ok {
		t.Errorf("temperature must not be sent")
	}
}

func TestOpusGetsServerSideFallbacks(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	p := fakeAPI(t, "end_turn", &body, &hdr)
	if _, err := p.Complete(context.Background(), "claude-opus-5", 500, gateway.Request{Messages: []gateway.Message{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if body["fallbacks"] != "default" {
		t.Errorf("fallbacks = %v, want \"default\"", body["fallbacks"])
	}
	if !strings.Contains(hdr.Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("beta header = %q", hdr.Get("anthropic-beta"))
	}
}

func TestRefusalAndTruncationAreErrors(t *testing.T) {
	for stop, want := range map[string]error{"refusal": gateway.ErrRefused, "max_tokens": gateway.ErrTruncated} {
		p := fakeAPI(t, stop, nil, nil)
		_, err := p.Complete(context.Background(), "claude-haiku-4-5", 10, gateway.Request{Messages: []gateway.Message{{Role: "user", Content: "x"}}})
		if !errors.Is(err, want) {
			t.Errorf("stop %s: got %v, want %v", stop, err, want)
		}
	}
}

func TestUnpricedModelRefused(t *testing.T) {
	p := New("k", map[string]Price{})
	if _, err := p.Complete(context.Background(), "claude-mystery", 10, gateway.Request{}); err == nil {
		t.Error("a model without a price must be refused, not metered at zero")
	}
}
