package email

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostmark(t *testing.T) {
	var batch []postmarkMessage
	status := http.StatusOK
	results := `[{"To":"a@koala.com.au","ErrorCode":0},{"To":"b@koala.com.au","ErrorCode":0}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/email/batch" || r.Header.Get("X-Postmark-Server-Token") != "tok" {
			t.Errorf("request %s token %q", r.URL.Path, r.Header.Get("X-Postmark-Server-Token"))
		}
		_ = json.NewDecoder(r.Body).Decode(&batch)
		w.WriteHeader(status)
		w.Write([]byte(results))
	}))
	defer srv.Close()
	p := &Postmark{Token: "tok", From: "Vellatry <reports@vellatry.com>", BaseURL: srv.URL}
	m := Message{To: []string{"a@koala.com.au", "b@koala.com.au"}, Subject: "Weekly", Text: "hi", HTML: "<p>hi</p>", Tag: "digest"}
	ctx := context.Background()

	if err := p.Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || batch[0].To != "a@koala.com.au" || batch[1].To != "b@koala.com.au" || batch[0].MessageStream != "outbound" || batch[0].TrackOpens {
		t.Errorf("batch = %+v; want one message per recipient, no open tracking", batch)
	}

	results = `[{"To":"a@koala.com.au","ErrorCode":0},{"To":"b@koala.com.au","ErrorCode":406,"Message":"inactive recipient"}]`
	if err := p.Send(ctx, m); !errors.Is(err, ErrRejected) {
		t.Errorf("partial failure: err = %v, want ErrRejected (no retry: a would get it twice)", err)
	}
	status, results = http.StatusUnprocessableEntity, `{"ErrorCode":300}`
	if err := p.Send(ctx, m); !errors.Is(err, ErrRejected) {
		t.Errorf("422: err = %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := p.Send(ctx, m); err == nil || errors.Is(err, ErrRejected) {
		t.Errorf("503 should be transient, got %v", err)
	}
	if err := p.Send(ctx, Message{}); err != nil {
		t.Errorf("no recipients: %v", err)
	}
}
