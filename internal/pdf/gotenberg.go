// Package pdf turns report HTML into PDF through Gotenberg (headless Chromium), which
// Vellatry runs itself. The HTML is always Vellatry's own rendered report with no
// external resources, so Chromium is never pointed at a customer-supplied URL.
package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// ErrRejected means Gotenberg refused the document; retrying will not help.
var ErrRejected = errors.New("pdf: rejected")

// Gotenberg converts HTML with a Gotenberg server.
type Gotenberg struct {
	URL  string // e.g. http://gotenberg:3000
	HTTP *http.Client
}

// HTMLToPDF renders one self-contained HTML document. Page size and margins come from
// the document's own @page rule.
func (g Gotenberg) HTMLToPDF(ctx context.Context, html []byte) ([]byte, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("files", "index.html")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(html); err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"preferCssPageSize": "true", "printBackground": "true", "skipNetworkIdleEvent": "true"} {
		if err := w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.URL, "/")+"/forms/chromium/convert/html", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pdf: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, fmt.Errorf("pdf: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusOK && bytes.HasPrefix(out, []byte("%PDF")):
		return out, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: status %d", ErrRejected, resp.StatusCode)
	}
	return nil, fmt.Errorf("pdf: status %d", resp.StatusCode) // retried
}
