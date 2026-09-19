// Package slack posts messages to Slack incoming webhooks. Outbound only: Vellatry has
// no Slack bot and reads nothing from Slack.
package slack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrGone means Slack no longer accepts the webhook (removed, channel archived or
	// deleted, app uninstalled). Retrying will not help; someone must add a new one.
	ErrGone = errors.New("slack: the webhook no longer works")
	// ErrRejected means Slack refused the message itself. That is our bug, not a
	// reason to retry.
	ErrRejected = errors.New("slack: message rejected")
)

// Webhook posts to incoming webhooks.
type Webhook struct {
	HTTP *http.Client
}

// Post sends payload (an incoming-webhook JSON body) to url. The URL must already have
// passed automation.ValidSlackWebhook; Post checks the host again anyway.
func (w Webhook) Post(ctx context.Context, url string, payload []byte) error {
	if !strings.HasPrefix(url, "https://hooks.slack.com/services/") {
		return fmt.Errorf("%w: not a Slack webhook URL", ErrGone)
	}
	client := w.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("slack: %w", err) // transient: retried by the job
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	reason := strings.TrimSpace(string(body))
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		return fmt.Errorf("%w (%s)", ErrGone, reason) // invalid_token, no_service, channel_not_found, channel_is_archived
	case resp.StatusCode == http.StatusBadRequest:
		return fmt.Errorf("%w (%s)", ErrRejected, reason)
	}
	return fmt.Errorf("slack: status %d (%s)", resp.StatusCode, reason) // 429 and 5xx: retry
}
