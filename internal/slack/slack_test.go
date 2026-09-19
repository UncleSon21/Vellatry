package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// redirect sends every request to the test server, keeping the path.
type redirect struct{ target *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestPost(t *testing.T) {
	var got []byte
	status, body := http.StatusOK, "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		if r.URL.Path != "/services/T/B/X" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request %s %s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	hook := Webhook{HTTP: &http.Client{Transport: redirect{u}}}
	ctx := context.Background()
	const endpoint = "https://hooks.slack.com/services/T/B/X"

	if err := hook.Post(ctx, endpoint, []byte(`{"text":"hi"}`)); err != nil || string(got) != `{"text":"hi"}` {
		t.Fatalf("post = %v, body %s", err, got)
	}
	for code, want := range map[int]error{http.StatusNotFound: ErrGone, http.StatusGone: ErrGone, http.StatusForbidden: ErrGone, http.StatusBadRequest: ErrRejected} {
		status, body = code, "no_service"
		if err := hook.Post(ctx, endpoint, []byte(`{}`)); !errors.Is(err, want) {
			t.Errorf("status %d: err = %v, want %v", code, err, want)
		}
	}
	status = http.StatusTooManyRequests
	if err := hook.Post(ctx, endpoint, []byte(`{}`)); err == nil || errors.Is(err, ErrGone) || errors.Is(err, ErrRejected) {
		t.Errorf("429 should be a transient error, got %v", err)
	}
	if err := hook.Post(ctx, "https://evil.example/services/T/B/X", nil); !errors.Is(err, ErrGone) {
		t.Errorf("non-Slack URL: err = %v", err)
	}
}
