package embed

import (
	"context"
	"os"
	"testing"
	"time"
)

// A smoke test against a real embedding service (ml/embed), skipped unless EMBED_URL is
// set. It checks the one property the product relies on: the same topic said two ways
// scores above SameTopic, and a related but different topic scores below it.
//
//	python -m uvicorn app:app --port 8099 --app-dir ml/embed
//	EMBED_URL=http://localhost:8099 go test ./internal/embed/ -run Live -v
func TestLiveService(t *testing.T) {
	url := os.Getenv("EMBED_URL")
	if url == "" {
		t.Skip("set EMBED_URL to run against a real embedding service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	texts := []string{"hybrid mattresses", "hybrid mattress", "pillows", "business energy plans"}
	got, err := (&Client{URL: url}).Embed(ctx, texts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model == "" || got.Dim != len(got.Vectors[0]) || len(got.Vectors) != len(texts) {
		t.Fatalf("model %q, dim %d, %d vectors", got.Model, got.Dim, len(got.Vectors))
	}

	same := Cosine(got.Vectors[0], got.Vectors[1])
	related := Cosine(got.Vectors[0], got.Vectors[2])
	unrelated := Cosine(got.Vectors[0], got.Vectors[3])
	t.Logf("%s: same %.3f, related %.3f, unrelated %.3f", got.Model, same, related, unrelated)

	if same < SameTopic {
		t.Errorf("the same topic said twice scored %.3f, below SameTopic (%.2f): the threshold no longer fits this model", same, SameTopic)
	}
	if related >= SameTopic {
		t.Errorf("a different topic scored %.3f, at or above SameTopic (%.2f): it would be suggested as a duplicate", related, SameTopic)
	}
	if unrelated >= related {
		t.Errorf("unrelated (%.3f) scored at least as high as related (%.3f)", unrelated, related)
	}
}
