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
	"strings"
	"time"

	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/platform/halt"
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
	BaseURL         string          // default DefaultBaseURL
	HTTPClient      *http.Client    // default http.DefaultClient
	Concurrency     int             // max requests in flight, default 4
	CallTimeout     time.Duration   // per HTTP attempt, default 150s (live LLM tasks run up to 120s)
	MaxRetries      int             // transient retries per call, default 3, negative disables
	Budget          budget.Reserver // required; paid calls fail with budget.ErrExceeded before sending
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
		e := &APIError{HTTPStatus: resp.StatusCode, Message: snippet(raw)}
		if reason, fatal := accountFatal(resp.StatusCode, e.Message); fatal {
			return nil, halt.Account("dataforseo", reason, e)
		}
		return nil, e
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &APIError{HTTPStatus: resp.StatusCode, Message: "decode: " + err.Error()}
	}
	if env.StatusCode != 20000 {
		e := &APIError{
			HTTPStatus: resp.StatusCode,
			StatusCode: env.StatusCode,
			Message:    env.StatusMessage,
			Transient:  env.StatusCode >= 50000,
		}
		// DataForSEO answers 200 with the real outcome in the envelope, so an empty
		// account arrives here rather than as an HTTP status.
		if reason, fatal := accountFatal(resp.StatusCode, e.Message); fatal {
			return nil, halt.Account("dataforseo", reason, e)
		}
		return nil, e
	}
	return &env, nil
}

// accountFatal reports whether a failure means Vellatry's DataForSEO account cannot be
// used at all, rather than this one call failing. It is deliberately narrow: a bad
// request or a missing task is this call's problem, and halting on it would stop the
// whole queue over one malformed request.
func accountFatal(httpStatus int, message string) (reason string, fatal bool) {
	switch httpStatus {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "the credentials were rejected", true
	case http.StatusPaymentRequired:
		return "the account is out of credit", true
	}
	m := strings.ToLower(message)
	for _, phrase := range []string{"insufficient funds", "not enough money", "payment required", "no money", "balance"} {
		if strings.Contains(m, phrase) {
			return "the account is out of credit", true
		}
	}
	for _, phrase := range []string{"unauthorized", "invalid credentials", "access denied", "authentication"} {
		if strings.Contains(m, phrase) {
			return "the credentials were rejected", true
		}
	}
	return "", false
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
