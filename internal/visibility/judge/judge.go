// Package judge scores the opinion half of the rubric for one AI answer that mentions
// the brand. The facts (mentions, position, citations, competitors) are computed by
// code in internal/visibility/detect; the judge never counts anything.
//
// The judge runs through the gateway's per-item "judge" purpose, which is refused
// unless explicitly allowed. It is the transitional teacher: every verdict is stored
// with its evidence and becomes training data for the trained judge that replaces it.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/UncleSon21/vellatry/internal/platform/gateway"
)

// Version identifies the rubric and prompt. Stored with every judgment; bump on change.
const Version = "rubric-1"

// Input is one answer to judge.
type Input struct {
	Brand           string
	Aliases         []string
	Competitors     []string
	Differentiators []string
	Question        string
	Answer          string
}

// Scaled is a 1-5 label with the sentence it rests on.
type Scaled struct {
	Label    int    `json:"label"`
	Evidence string `json:"evidence"`
}

// Criticism is whether the answer criticises the brand unprompted.
type Criticism struct {
	Present  bool   `json:"present"`
	Evidence string `json:"evidence"`
}

// Role is how the answer positions the brand.
type Role struct {
	Value    string `json:"value"`
	Evidence string `json:"evidence"`
}

// Claim is whether the answer supports one of the brand's declared differentiators.
type Claim struct {
	Claim     string `json:"claim"`
	Supported bool   `json:"supported"`
	Evidence  string `json:"evidence"`
}

// Result is the judged half of the rubric.
type Result struct {
	Tone                Scaled    `json:"tone"`
	ComparativeFraming  Scaled    `json:"comparative_framing"`
	UnpromptedCriticism Criticism `json:"unprompted_criticism"`
	Role                Role      `json:"role"`
	Hedging             Scaled    `json:"hedging"`
	Specificity         Scaled    `json:"specificity"`
	Consistency         Scaled    `json:"consistency"`
	Directness          Scaled    `json:"directness"`
	QuestionCoverage    Scaled    `json:"question_coverage"`
	Depth               Scaled    `json:"depth"`
	EvidenceQuality     Scaled    `json:"evidence_quality"`
	Recency             Scaled    `json:"recency"`
	Differentiators     []Claim   `json:"differentiators"`
	Uncertainty         bool      `json:"uncertainty"`
	Contradiction       bool      `json:"contradiction"`
}

// Roles, from best to worst for the brand.
var Roles = []string{"recommended_first", "recommended_option", "neutral_mention", "compared_unfavourably", "warned_against"}

var (
	// ErrTooLong means the answer exceeds the judge's input limit; it is left unjudged
	// rather than truncated.
	ErrTooLong = errors.New("judge: answer too long to judge without truncation")
	// ErrInvalid means the reply failed validation.
	ErrInvalid = errors.New("judge: invalid verdict")
)

// Completer is the gateway.
type Completer interface {
	Complete(ctx context.Context, req gateway.Request) (gateway.Response, error)
}

// Judge scores in for orgID. Evidence that is not a verbatim quote of the answer is
// discarded, so a stored evidence string always points at real text.
func Judge(ctx context.Context, g Completer, orgID string, in Input) (Result, gateway.Response, error) {
	resp, err := g.Complete(ctx, gateway.Request{
		Purpose:  "judge",
		OrgID:    orgID,
		System:   systemPrompt,
		Messages: []gateway.Message{{Role: "user", Content: userMessage(in)}},
		Schema:   Schema(len(in.Differentiators) > 0),
	})
	if errors.Is(err, gateway.ErrPromptTooLarge) {
		return Result{}, resp, ErrTooLong
	}
	if err != nil {
		return Result{}, resp, err
	}
	var r Result
	if err := json.Unmarshal([]byte(resp.Text), &r); err != nil {
		return Result{}, resp, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := validate(&r, in); err != nil {
		return Result{}, resp, err
	}
	return r, resp, nil
}

func validate(r *Result, in Input) error {
	for name, s := range r.scaled() {
		if s.Label < 1 || s.Label > 5 {
			return fmt.Errorf("%w: %s label %d", ErrInvalid, name, s.Label)
		}
		s.Evidence = grounded(s.Evidence, in.Answer)
	}
	if !contains(Roles, r.Role.Value) {
		return fmt.Errorf("%w: role %q", ErrInvalid, r.Role.Value)
	}
	r.Role.Evidence = grounded(r.Role.Evidence, in.Answer)
	r.UnpromptedCriticism.Evidence = grounded(r.UnpromptedCriticism.Evidence, in.Answer)

	// Keep only verdicts for claims the brand actually declared, in declared order.
	byKey := map[string]Claim{}
	for _, c := range r.Differentiators {
		byKey[normalise(c.Claim)] = c
	}
	var claims []Claim
	for _, d := range in.Differentiators {
		c, ok := byKey[normalise(d)]
		if !ok {
			continue
		}
		c.Claim = d
		c.Evidence = grounded(c.Evidence, in.Answer)
		if c.Supported && c.Evidence == "" {
			c.Supported = false // a claim counts as supported only with a real quote
		}
		claims = append(claims, c)
	}
	r.Differentiators = claims
	return nil
}

func (r *Result) scaled() map[string]*Scaled {
	return map[string]*Scaled{
		"tone": &r.Tone, "comparative_framing": &r.ComparativeFraming,
		"hedging": &r.Hedging, "specificity": &r.Specificity, "consistency": &r.Consistency, "directness": &r.Directness,
		"question_coverage": &r.QuestionCoverage, "depth": &r.Depth, "evidence_quality": &r.EvidenceQuality, "recency": &r.Recency,
	}
}

// grounded returns quote if it appears verbatim in answer (ignoring surrounding space
// and case), else "".
func grounded(quote, answer string) string {
	q := strings.TrimSpace(quote)
	if q == "" || !strings.Contains(strings.ToLower(answer), strings.ToLower(q)) {
		return ""
	}
	return q
}

func normalise(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
