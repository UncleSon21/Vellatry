// Package email sends transactional email: alerts, the weekly digest, report links and
// sign-in links. Postmark in production; a logging sender for local development.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Message is one email. Each recipient gets their own copy, so recipients never see
// each other's addresses.
type Message struct {
	To      []string
	Subject string
	Text    string
	HTML    string
	Tag     string // for the provider's stats: alert, digest, report, sign_in
}

// Sender sends email.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// ErrRejected means the provider refused the message or a recipient for a reason
// retrying will not fix (bad address, suppressed recipient, sender not verified).
var ErrRejected = errors.New("email: rejected")

// Postmark sends through Postmark's batch endpoint.
type Postmark struct {
	Token   string // server API token
	From    string // a verified sender signature, e.g. "Vellatry <reports@vellatry.com>"
	Stream  string // message stream; "outbound" by default
	HTTP    *http.Client
	BaseURL string // tests only
}

type postmarkMessage struct {
	From          string `json:"From"`
	To            string `json:"To"`
	Subject       string `json:"Subject"`
	TextBody      string `json:"TextBody,omitempty"`
	HtmlBody      string `json:"HtmlBody,omitempty"`
	Tag           string `json:"Tag,omitempty"`
	MessageStream string `json:"MessageStream"`
	TrackOpens    bool   `json:"TrackOpens"`
}

type postmarkResult struct {
	To        string `json:"To"`
	ErrorCode int    `json:"ErrorCode"`
	Message   string `json:"Message"`
}

// Send sends one copy per recipient.
func (p *Postmark) Send(ctx context.Context, m Message) error {
	if len(m.To) == 0 {
		return nil
	}
	if len(m.To) > 500 {
		return fmt.Errorf("%w: too many recipients", ErrRejected)
	}
	stream := p.Stream
	if stream == "" {
		stream = "outbound"
	}
	batch := make([]postmarkMessage, len(m.To))
	for i, to := range m.To {
		batch[i] = postmarkMessage{From: p.From, To: to, Subject: m.Subject, TextBody: m.Text, HtmlBody: m.HTML, Tag: m.Tag, MessageStream: stream}
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	base := p.BaseURL
	if base == "" {
		base = "https://api.postmarkapp.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/email/batch", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Postmark-Server-Token", p.Token)
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("email: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, truncate(string(raw)))
	default:
		return fmt.Errorf("email: status %d", resp.StatusCode) // 429, 5xx: retry
	}
	var results []postmarkResult
	if err := json.Unmarshal(raw, &results); err != nil {
		return fmt.Errorf("email: unreadable response: %w", err)
	}
	var failed []string
	for _, r := range results {
		if r.ErrorCode != 0 {
			failed = append(failed, fmt.Sprintf("%s (%s)", r.To, r.Message))
		}
	}
	if len(failed) > 0 {
		// Some copies may have gone; retrying would send them twice.
		return fmt.Errorf("%w: %s", ErrRejected, strings.Join(failed, "; "))
	}
	return nil
}

func truncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// Log writes email to the log instead of sending it. Local development only.
type Log struct {
	Logger *slog.Logger
}

// Send logs the message.
func (l Log) Send(ctx context.Context, m Message) error {
	l.Logger.InfoContext(ctx, "email (not sent: no provider configured)", "to", m.To, "subject", m.Subject, "tag", m.Tag, "text", m.Text)
	return nil
}
