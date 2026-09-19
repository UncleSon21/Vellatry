package pdf

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGotenberg(t *testing.T) {
	status := http.StatusOK
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/forms/chromium/convert/html" {
			t.Errorf("path %s", r.URL.Path)
		}
		f, h, err := r.FormFile("files")
		if err != nil || h.Filename != "index.html" {
			t.Errorf("form file: %v %v", h, err)
		} else {
			got, _ = io.ReadAll(f)
		}
		if r.FormValue("preferCssPageSize") != "true" || r.FormValue("printBackground") != "true" {
			t.Error("page size and background options missing")
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			w.Write([]byte("%PDF-1.7 fake"))
		}
	}))
	defer srv.Close()
	g := Gotenberg{URL: srv.URL + "/"}
	ctx := context.Background()

	out, err := g.HTMLToPDF(ctx, []byte("<html>report</html>"))
	if err != nil || string(out) != "%PDF-1.7 fake" || string(got) != "<html>report</html>" {
		t.Fatalf("pdf = %q, %v; sent %q", out, err, got)
	}
	status = http.StatusBadRequest
	if _, err := g.HTMLToPDF(ctx, nil); !errors.Is(err, ErrRejected) {
		t.Errorf("400: %v", err)
	}
	status = http.StatusServiceUnavailable
	if _, err := g.HTMLToPDF(ctx, nil); err == nil || errors.Is(err, ErrRejected) {
		t.Errorf("503 should be transient: %v", err)
	}
}
