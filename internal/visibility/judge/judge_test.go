package judge

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/UncleSon21/vellatry/internal/platform/gateway"
)

type fakeGateway struct {
	reply string
	err   error
	got   gateway.Request
}

func (f *fakeGateway) Complete(_ context.Context, req gateway.Request) (gateway.Response, error) {
	f.got = req
	return gateway.Response{Text: f.reply}, f.err
}

const answer = "Koala is a popular mattress with free delivery. Ecosa is cheaper. Some buyers find Koala too firm."

var input = Input{
	Brand: "Koala", Competitors: []string{"Ecosa"},
	Differentiators: []string{"Free delivery", "Made in Australia"},
	Question:        "best mattress?", Answer: answer,
}

func verdict(mut func(m map[string]any)) string {
	scaled := func(l int, ev string) map[string]any { return map[string]any{"label": l, "evidence": ev} }
	m := map[string]any{
		"tone":                 scaled(4, "Koala is a popular mattress with free delivery."),
		"comparative_framing":  scaled(2, "Ecosa is cheaper."),
		"unprompted_criticism": map[string]any{"present": true, "evidence": "Some buyers find Koala too firm."},
		"role":                 map[string]any{"value": "recommended_option", "evidence": "invented sentence not in the answer"},
		"hedging":              scaled(3, ""), "specificity": scaled(3, ""), "consistency": scaled(5, ""), "directness": scaled(4, ""),
		"question_coverage": scaled(4, ""), "depth": scaled(2, ""), "evidence_quality": scaled(2, ""), "recency": scaled(3, ""),
		"uncertainty": false, "contradiction": false,
		"differentiators": []map[string]any{
			{"claim": "free delivery", "supported": true, "evidence": "free delivery"},
			{"claim": "Made in Australia", "supported": true, "evidence": "made in Sydney"}, // not in the answer
			{"claim": "Invented claim", "supported": true, "evidence": ""},
		},
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestJudgeGroundsEvidenceAndClaims(t *testing.T) {
	g := &fakeGateway{reply: verdict(nil)}
	r, _, err := Judge(context.Background(), g, "org", input)
	if err != nil {
		t.Fatal(err)
	}
	if g.got.Purpose != "judge" || g.got.Schema == nil || g.got.System == "" {
		t.Errorf("request = %+v", g.got)
	}
	if !strings.Contains(g.got.Messages[0].Content, "<answer>\n"+answer) {
		t.Errorf("answer not passed whole: %q", g.got.Messages[0].Content)
	}
	if r.Role.Evidence != "" {
		t.Errorf("invented evidence kept: %q", r.Role.Evidence)
	}
	if r.Tone.Evidence == "" {
		t.Error("verbatim evidence dropped")
	}
	if len(r.Differentiators) != 2 {
		t.Fatalf("claims = %+v, want the two declared ones only", r.Differentiators)
	}
	if !r.Differentiators[0].Supported || r.Differentiators[0].Claim != "Free delivery" {
		t.Errorf("claim 0 = %+v", r.Differentiators[0])
	}
	if r.Differentiators[1].Supported {
		t.Errorf("a claim with no real quote must not count as supported: %+v", r.Differentiators[1])
	}
	if !FramingNegative(r) {
		t.Error("framing 2 and criticism present should be negative framing")
	}
	if DifferentiatorGap(r, 2) {
		t.Error("one supported claim means no differentiator gap")
	}
}

func TestJudgeRejectsInvalidVerdicts(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"label out of range": func(m map[string]any) { m["tone"] = map[string]any{"label": 7, "evidence": ""} },
		"unknown role":       func(m map[string]any) { m["role"] = map[string]any{"value": "best_ever", "evidence": ""} },
	}
	for name, mut := range cases {
		g := &fakeGateway{reply: verdict(mut)}
		if _, _, err := Judge(context.Background(), g, "org", input); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	g := &fakeGateway{reply: "not json"}
	if _, _, err := Judge(context.Background(), g, "org", input); !errors.Is(err, ErrInvalid) {
		t.Errorf("non-JSON reply: got %v", err)
	}
}

func TestJudgeTooLongIsNotTruncated(t *testing.T) {
	g := &fakeGateway{err: gateway.ErrPromptTooLarge}
	if _, _, err := Judge(context.Background(), g, "org", input); !errors.Is(err, ErrTooLong) {
		t.Errorf("got %v, want ErrTooLong", err)
	}
}

func TestSchemaIsClosedEverywhere(t *testing.T) {
	var walk func(path string, v any)
	walk = func(path string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if m["type"] == "object" {
			if m["additionalProperties"] != false {
				t.Errorf("%s: object must set additionalProperties false", path)
			}
			props := m["properties"].(map[string]any)
			if len(m["required"].([]string)) != len(props) {
				t.Errorf("%s: every property must be required", path)
			}
			for k, p := range props {
				walk(path+"."+k, p)
			}
		}
		if items, ok := m["items"]; ok {
			walk(path+"[]", items)
		}
	}
	walk("$", Schema(true))
	if _, ok := Schema(false)["properties"].(map[string]any)["differentiators"]; ok {
		t.Error("no declared differentiators: the schema should not ask for them")
	}
}

func TestScores(t *testing.T) {
	if Score10(1) != 1 || Score10(3) != 5.5 || Score10(5) != 10 {
		t.Errorf("Score10 = %v %v %v", Score10(1), Score10(3), Score10(5))
	}
	r := Result{Tone: Scaled{Label: 5}, ComparativeFraming: Scaled{Label: 5}}
	if SentimentScore(r) != 10 {
		t.Errorf("all positive = %v", SentimentScore(r))
	}
	r.UnpromptedCriticism.Present = true
	if got := SentimentScore(r); math.Abs(got-7) > 1e-9 {
		t.Errorf("with criticism = %v, want 7", got)
	}
	rows := Rows(Result{Tone: Scaled{Label: 3}, ComparativeFraming: Scaled{Label: 3}, Hedging: Scaled{Label: 3}, Specificity: Scaled{Label: 3},
		Consistency: Scaled{Label: 3}, Directness: Scaled{Label: 3}, QuestionCoverage: Scaled{Label: 3}, Depth: Scaled{Label: 3},
		EvidenceQuality: Scaled{Label: 3}, Recency: Scaled{Label: 3}, Role: Role{Value: "neutral_mention"}})
	if len(rows) != 16 { // 10 scaled + criticism + role + flags + 3 family scores
		t.Errorf("rows = %d, want 16", len(rows))
	}
}
