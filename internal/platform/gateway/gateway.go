// Package gateway is the only way Vellatry calls an LLM.
//
// It exists to stop the failure that sank the previous system: LLM spend on paths that
// run once per answer, keyword or page, and prompts that grew without bound. Every call
// names a registered Purpose, and the gateway refuses, before anything is sent:
//
//   - an unknown purpose,
//   - a per-item purpose that has not been explicitly allowed,
//   - a prompt larger than its purpose's limit,
//   - a call that would take the tenant over its budget.
//
// Identical requests are served from cache and never paid for twice. Every call, cached
// or not, is metered to the tenant.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/UncleSon21/vellatry/internal/platform/budget"
)

var (
	ErrUnknownPurpose = errors.New("gateway: unknown purpose")
	ErrPerItemRefused = errors.New("gateway: per-item purpose is not allowed to call an LLM")
	ErrPromptTooLarge = errors.New("gateway: prompt exceeds the purpose's input limit")
	ErrNoOrg          = errors.New("gateway: request has no org")
)

// Purpose declares one reason the product calls an LLM.
type Purpose struct {
	Name            string
	PerItem         bool    // runs once per answer, keyword, page or query
	MaxInputChars   int     // system prompt + all messages
	MaxOutputTokens int     //
	Tier            string  // model tier, resolved through Config.Models
	EstimateUSD     float64 // per-call budget reservation until real costs are seen
}

// Message is one conversation turn.
type Message struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// Request is one LLM call.
type Request struct {
	Purpose  string
	OrgID    string
	System   string
	Messages []Message
}

// Usage is what a call consumed.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// Response is a completed call.
type Response struct {
	Text   string
	Model  string
	Usage  Usage
	Cached bool
}

// Provider sends a request to one LLM vendor. Implementations live in subpackages and
// use raw HTTP; vendor SDKs are not used.
type Provider interface {
	Complete(ctx context.Context, model string, maxOutputTokens int, req Request) (Response, error)
}

// Cache stores responses by request hash.
type Cache interface {
	Get(ctx context.Context, key string) (Response, bool, error)
	Put(ctx context.Context, key string, r Response) error
}

// Budgets returns the spend cap for a tenant.
type Budgets interface {
	For(orgID string) *budget.Budget
}

// Meter records usage against a tenant.
type Meter interface {
	Record(ctx context.Context, r MeterRecord) error
}

// MeterRecord is one metered call.
type MeterRecord struct {
	OrgID   string
	Purpose string
	Model   string
	Usage   Usage
	Cached  bool
}

// Config wires a Gateway.
type Config struct {
	Purposes     []Purpose
	AllowPerItem []string          // per-item purposes allowed to call anyway (design-partner judge only)
	Models       map[string]string // tier -> model id
	Provider     Provider
	Cache        Cache
	Budgets      Budgets
	Meter        Meter
}

// Gateway enforces the rules above around a Provider.
type Gateway struct {
	purposes map[string]Purpose
	allowed  map[string]bool
	cfg      Config
}

// New validates cfg and returns a Gateway.
func New(cfg Config) (*Gateway, error) {
	if cfg.Provider == nil || cfg.Cache == nil || cfg.Budgets == nil || cfg.Meter == nil {
		return nil, errors.New("gateway: provider, cache, budgets and meter are required")
	}
	g := &Gateway{purposes: map[string]Purpose{}, allowed: map[string]bool{}, cfg: cfg}
	for _, p := range cfg.Purposes {
		if _, dup := g.purposes[p.Name]; dup {
			return nil, fmt.Errorf("gateway: purpose %q registered twice", p.Name)
		}
		if p.MaxInputChars <= 0 || p.MaxOutputTokens <= 0 || p.EstimateUSD <= 0 {
			return nil, fmt.Errorf("gateway: purpose %q needs input, output and cost limits", p.Name)
		}
		if cfg.Models[p.Tier] == "" {
			return nil, fmt.Errorf("gateway: purpose %q uses tier %q with no model configured", p.Name, p.Tier)
		}
		g.purposes[p.Name] = p
	}
	for _, name := range cfg.AllowPerItem {
		p, ok := g.purposes[name]
		if !ok || !p.PerItem {
			return nil, fmt.Errorf("gateway: AllowPerItem names %q, which is not a registered per-item purpose", name)
		}
		g.allowed[name] = true
	}
	return g, nil
}

// Complete runs one request through the checks, the cache, the budget and the provider.
func (g *Gateway) Complete(ctx context.Context, req Request) (Response, error) {
	p, ok := g.purposes[req.Purpose]
	if !ok {
		return Response{}, fmt.Errorf("%w: %q", ErrUnknownPurpose, req.Purpose)
	}
	if p.PerItem && !g.allowed[p.Name] {
		return Response{}, fmt.Errorf("%w: %q", ErrPerItemRefused, p.Name)
	}
	if req.OrgID == "" {
		return Response{}, ErrNoOrg
	}
	if n := inputChars(req); n > p.MaxInputChars {
		return Response{}, fmt.Errorf("%w: %q is %d chars, limit %d", ErrPromptTooLarge, p.Name, n, p.MaxInputChars)
	}
	model := g.cfg.Models[p.Tier]
	key := cacheKey(p.Name, model, req)

	if r, hit, err := g.cfg.Cache.Get(ctx, key); err != nil {
		return Response{}, err
	} else if hit {
		r.Cached = true
		r.Usage.CostUSD = 0
		return r, g.cfg.Meter.Record(ctx, MeterRecord{OrgID: req.OrgID, Purpose: p.Name, Model: model, Cached: true})
	}

	release, err := g.cfg.Budgets.For(req.OrgID).Reserve(1)
	if err != nil {
		return Response{}, err
	}
	resp, err := g.cfg.Provider.Complete(ctx, model, p.MaxOutputTokens, req)
	if err != nil {
		release(0)
		return Response{}, err
	}
	release(resp.Usage.CostUSD)
	resp.Model = model
	if err := g.cfg.Cache.Put(ctx, key, resp); err != nil {
		return Response{}, err
	}
	return resp, g.cfg.Meter.Record(ctx, MeterRecord{OrgID: req.OrgID, Purpose: p.Name, Model: model, Usage: resp.Usage})
}

func inputChars(r Request) int {
	n := len(r.System)
	for _, m := range r.Messages {
		n += len(m.Content)
	}
	return n
}

func cacheKey(purpose, model string, r Request) string {
	b, _ := json.Marshal(struct {
		Purpose, Model, System string
		Messages               []Message
	}{purpose, model, r.System, r.Messages})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// MemoryCache is an in-process Cache. The Postgres-backed cache replaces it in M1.
type MemoryCache struct {
	mu sync.Mutex
	m  map[string]Response
}

func NewMemoryCache() *MemoryCache { return &MemoryCache{m: map[string]Response{}} }

func (c *MemoryCache) Get(_ context.Context, key string) (Response, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[key]
	return r, ok, nil
}

func (c *MemoryCache) Put(_ context.Context, key string, r Response) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = r
	return nil
}

// FixedBudgets gives every tenant the same in-process cap. Database-backed daily
// budgets per plan replace it in M1.
type FixedBudgets struct {
	LimitUSD, EstimateUSD float64
	mu                    sync.Mutex
	m                     map[string]*budget.Budget
}

func (f *FixedBudgets) For(orgID string) *budget.Budget {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]*budget.Budget{}
	}
	b, ok := f.m[orgID]
	if !ok {
		b = budget.New(f.LimitUSD, f.EstimateUSD)
		f.m[orgID] = b
	}
	return b
}
