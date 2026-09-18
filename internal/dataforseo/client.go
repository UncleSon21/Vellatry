// Package dataforseo is the only client for DataForSEO. Every paid call reserves
// budget first and fails closed, requests are paced by a concurrency limit, each call
// has its own timeout, and only transient errors are retried.
package dataforseo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/UncleSon21/vellatry/internal/platform/budget"
)

// DefaultBaseURL is the production API host.
const DefaultBaseURL = "https://api.dataforseo.com"

// APIError is a failed request or task.
type APIError struct {
	HTTPStatus int
	StatusCode int // DataForSEO status_code, 0 if none
	Message    string
	Transient  bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("dataforseo: http %d, status %d: %s", e.HTTPStatus, e.StatusCode, e.Message)
}

// IsTransient reports whether err is worth retrying later.
func IsTransient(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Transient
}

// Config configures a Client.
type Config struct {
	Login, Password string
	BaseURL         string         // default DefaultBaseURL
	HTTPClient      *http.Client   // default http.DefaultClient
	Concurrency     int            // max requests in flight, default 4
	CallTimeout     time.Duration  // per HTTP attempt, default 150s (live LLM tasks run up to 120s)
	MaxRetries      int            // transient retries per call, default 3, negative disables
	Budget          *budget.Budget // required; paid calls fail with budget.ErrExceeded before sending
}

// Client talks to DataForSEO.
type Client struct {
	cfg   Config
	sem   chan struct{}
	sleep func(context.Context, time.Duration) error
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Login == "" || cfg.Password == "" {
		return nil, errors.New("dataforseo: login and password are required")
	}
	if cfg.Budget == nil {
		return nil, errors.New("dataforseo: a budget is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 150 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	} else if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	return &Client{cfg: cfg, sem: make(chan struct{}, cfg.Concurrency), sleep: sleepCtx}, nil
}

// envelope is the response wrapper shared by every endpoint.
type envelope struct {
	StatusCode    int     `json:"status_code"`
	StatusMessage string  `json:"status_message"`
	Cost          float64 `json:"cost"`
	Tasks         []task  `json:"tasks"`
}

type task struct {
	ID            string          `json:"id"`
	StatusCode    int             `json:"status_code"`
	StatusMessage string          `json:"status_message"`
	Cost          float64         `json:"cost"`
	Data          json.RawMessage `json:"data"`
	Result        json.RawMessage `json:"result"`
}

// call performs one request. paid is the number of billable tasks it creates (0 for
// free endpoints such as task_get); budget is reserved for them before sending.
func (c *Client) call(ctx context.Context, method, path string, body any, paid int) (*envelope, error) {
	release := func(float64) {}
	if paid > 0 {
		r, err := c.cfg.Budget.Reserve(paid)
		if err != nil {
			return nil, err
		}
		release = r
	}
	charged := 0.0
	defer func() { release(charged) }()

	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()

	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
		}
		env, err := c.once(ctx, method, path, payload)
		if err == nil {
			charged = env.Cost
			return env, nil
		}
		lastErr = err
		if !IsTransient(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) once(ctx context.Context, method, path string, payload []byte) (*envelope, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.CallTimeout)
	defer cancel()
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.cfg.Login, c.cfg.Password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		// Network failures and per-call timeouts are weather, not bugs.
		return nil, &APIError{Message: err.Error(), Transient: true}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &APIError{HTTPStatus: resp.StatusCode, Message: err.Error(), Transient: true}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, &APIError{HTTPStatus: resp.StatusCode, Message: snippet(raw), Transient: true}
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{HTTPStatus: resp.StatusCode, Message: snippet(raw)}
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &APIError{HTTPStatus: resp.StatusCode, Message: "decode: " + err.Error()}
	}
	if env.StatusCode != 20000 {
		return nil, &APIError{
			HTTPStatus: resp.StatusCode,
			StatusCode: env.StatusCode,
			Message:    env.StatusMessage,
			Transient:  env.StatusCode >= 50000,
		}
	}
	return &env, nil
}

func backoff(attempt int) time.Duration {
	base := time.Duration(1<<attempt) * time.Second // 2s, 4s, 8s
	return base + time.Duration(rand.Int64N(int64(base/2)))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func snippet(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return string(b)
}
