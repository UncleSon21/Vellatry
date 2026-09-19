// Package asana creates and closes Asana tasks for fixes and blindspots. Outbound only:
// Vellatry never reads a customer's tasks beyond the ones it created.
package asana

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/UncleSon21/vellatry/internal/asanaauth"
)

var (
	// ErrAuth means the grant is gone (revoked, expired, app removed): the connection
	// is broken until someone reconnects it.
	ErrAuth = errors.New("asana: authorisation no longer valid")
	// ErrNotFound means the task or project does not exist or is not visible.
	ErrNotFound = errors.New("asana: not found")
	// ErrRejected means Asana refused the request (bad input, no access to the project).
	ErrRejected = errors.New("asana: request rejected")
)

// Ref is a named Asana object.
type Ref struct {
	GID       string `json:"gid"`
	Name      string `json:"name"`
	Workspace string `json:"workspace,omitempty"`
}

// Task is the part of a task Vellatry needs.
type Task struct {
	GID       string `json:"gid"`
	URL       string `json:"permalink_url"`
	Completed bool   `json:"completed"`
}

// NewTask is a task to create.
type NewTask struct {
	Project    string
	Name       string
	Notes      string
	ExternalID string // makes creation idempotent: the task can be found by it later
}

// Client calls the Asana API as one grant.
type Client struct {
	HTTP    *http.Client
	BaseURL string // tests only
}

// New returns a client that refreshes its access token from refreshToken.
func New(ctx context.Context, cfg *oauth2.Config, refreshToken string) *Client {
	hc := cfg.Client(ctx, &oauth2.Token{RefreshToken: refreshToken})
	hc.Timeout = 20 * time.Second
	return &Client{HTTP: hc}
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://app.asana.com/api/1.0"
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(map[string]any{"data": in})
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && (re.Response == nil || re.Response.StatusCode < 500) {
			return fmt.Errorf("%w: %s", ErrAuth, re.ErrorCode)
		}
		return fmt.Errorf("asana: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrAuth
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("asana: status %d", resp.StatusCode) // retried by the job
	default:
		return fmt.Errorf("%w: status %d: %s", ErrRejected, resp.StatusCode, asanaMessage(raw))
	}
	if out == nil {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("asana: unreadable response: %w", err)
	}
	return json.Unmarshal(env.Data, out)
}

func asanaMessage(raw []byte) string {
	var e struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &e) == nil && len(e.Errors) > 0 {
		return e.Errors[0].Message
	}
	return ""
}

// Workspaces lists the workspaces the grant can see.
func (c *Client) Workspaces(ctx context.Context) ([]Ref, error) {
	var out []Ref
	return out, c.do(ctx, http.MethodGet, "/workspaces?limit=100&opt_fields=name", nil, &out)
}

// Projects lists a workspace's unarchived projects (the first 100).
func (c *Client) Projects(ctx context.Context, workspace string) ([]Ref, error) {
	var out []Ref
	err := c.do(ctx, http.MethodGet, "/projects?limit=100&archived=false&opt_fields=name&workspace="+url.QueryEscape(workspace), nil, &out)
	for i := range out {
		out[i].Workspace = workspace
	}
	return out, err
}

const taskFields = "?opt_fields=permalink_url,completed"

// TaskByExternal finds a task this app created with externalID.
func (c *Client) TaskByExternal(ctx context.Context, externalID string) (Task, bool, error) {
	var t Task
	err := c.do(ctx, http.MethodGet, "/tasks/external:"+url.PathEscape(externalID)+taskFields, nil, &t)
	if errors.Is(err, ErrNotFound) {
		return t, false, nil
	}
	return t, err == nil, err
}

// CreateTask creates a task in a project.
func (c *Client) CreateTask(ctx context.Context, nt NewTask) (Task, error) {
	var t Task
	in := map[string]any{
		"name": nt.Name, "notes": nt.Notes, "projects": []string{nt.Project},
		"external": map[string]string{"gid": nt.ExternalID},
	}
	return t, c.do(ctx, http.MethodPost, "/tasks"+taskFields, in, &t)
}

// SetCompleted completes or reopens a task.
func (c *Client) SetCompleted(ctx context.Context, gid string, completed bool) error {
	return c.do(ctx, http.MethodPut, "/tasks/"+url.PathEscape(gid), map[string]bool{"completed": completed}, nil)
}

// Comment adds a comment to a task.
func (c *Client) Comment(ctx context.Context, gid, text string) error {
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(gid)+"/stories", map[string]string{"text": text}, nil)
}

// Revoke revokes a refresh token.
func Revoke(ctx context.Context, hc *http.Client, cfg *oauth2.Config, refreshToken string) error {
	form := url.Values{"client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}, "token": {refreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, asanaauth.RevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("asana: revoke status %d", resp.StatusCode)
	}
	return nil // 4xx: already revoked or unknown; either way it no longer works
}
