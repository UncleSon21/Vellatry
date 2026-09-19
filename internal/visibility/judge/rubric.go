package judge

import (
	"fmt"
	"sort"
	"strings"
)

// systemPrompt is identical for every tenant and answer, so it is a stable cache prefix.
// Brand-specific content goes in the user message.
const systemPrompt = `You score how an AI assistant's answer treats one brand, for a brand-monitoring product. You judge only what the answer says. Do not use outside knowledge about the brand, and do not reward or penalise the answer for agreeing with you.

Every label is on a 1 to 5 scale with these anchors. Use 3 when the answer gives no signal for that dimension.

About the brand:
- tone: how the answer talks about the brand. 1 hostile or clearly negative, 2 mostly negative, 3 neutral or factual, 4 mostly positive, 5 enthusiastic endorsement.
- comparative_framing: how the brand is framed against the named competitors. 1 clearly presented as worse, 2 somewhat worse, 3 equal or not compared, 4 somewhat better, 5 clearly presented as the better choice.
- unprompted_criticism: present when the answer raises a drawback, complaint or risk about the brand that the question did not ask about.
- role: how the answer positions the brand. recommended_first (the top or primary recommendation), recommended_option (recommended among others), neutral_mention (mentioned without recommending), compared_unfavourably (mentioned mainly as the weaker alternative), warned_against (advises against it).

About the answer as a whole:
- hedging: 1 heavily hedged or noncommittal, 3 some qualifiers, 5 states its conclusions plainly.
- specificity: 1 vague generalities, 3 some concrete detail, 5 specific names, figures and features.
- consistency: 1 contradicts itself, 3 minor tension, 5 fully consistent.
- directness: 1 evades the question, 3 answers indirectly, 5 answers the question head on.
- question_coverage: 1 misses the question, 3 answers part of it, 5 answers all of it.
- depth: 1 superficial, 3 moderate, 5 thorough.
- evidence_quality: 1 unsupported assertions, 3 some support, 5 claims backed by sources, data or reasoning.
- recency: 1 clearly outdated information, 3 no time signal, 5 explicitly current information.
- uncertainty: true only when the answer explicitly says it lacks information or may be out of date.
- contradiction: true when the answer contradicts itself about the brand.

Differentiators: for each claim listed in the user message, say whether the answer supports it (states or clearly implies it about the brand). A claim the answer does not address is not supported.

Evidence rules: every "evidence" field must be copied word for word from the answer, one sentence or shorter. Use an empty string when there is nothing to quote, for example when a dimension is at 3 for lack of signal. Never paraphrase.`

func userMessage(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<brand>%s</brand>\n", in.Brand)
	if len(in.Aliases) > 0 {
		fmt.Fprintf(&b, "<brand_aliases>%s</brand_aliases>\n", strings.Join(in.Aliases, "; "))
	}
	if len(in.Competitors) > 0 {
		fmt.Fprintf(&b, "<competitors>%s</competitors>\n", strings.Join(in.Competitors, "; "))
	}
	if len(in.Differentiators) > 0 {
		b.WriteString("<differentiators>\n")
		for _, d := range in.Differentiators {
			fmt.Fprintf(&b, "- %s\n", d)
		}
		b.WriteString("</differentiators>\n")
	}
	fmt.Fprintf(&b, "<question>%s</question>\n<answer>\n%s\n</answer>", in.Question, in.Answer)
	return b.String()
}

// Schema is the JSON Schema the reply must follow (structured outputs).
func Schema(withDifferentiators bool) map[string]any {
	scaled := func() map[string]any {
		return object(map[string]any{
			"label":    map[string]any{"type": "integer", "enum": []int{1, 2, 3, 4, 5}},
			"evidence": map[string]any{"type": "string"},
		})
	}
	props := map[string]any{
		"tone":                scaled(),
		"comparative_framing": scaled(),
		"unprompted_criticism": object(map[string]any{
			"present":  map[string]any{"type": "boolean"},
			"evidence": map[string]any{"type": "string"},
		}),
		"role": object(map[string]any{
			"value":    map[string]any{"type": "string", "enum": Roles},
			"evidence": map[string]any{"type": "string"},
		}),
		"hedging":           scaled(),
		"specificity":       scaled(),
		"consistency":       scaled(),
		"directness":        scaled(),
		"question_coverage": scaled(),
		"depth":             scaled(),
		"evidence_quality":  scaled(),
		"recency":           scaled(),
		"uncertainty":       map[string]any{"type": "boolean"},
		"contradiction":     map[string]any{"type": "boolean"},
	}
	if withDifferentiators {
		props["differentiators"] = map[string]any{
			"type": "array",
			"items": object(map[string]any{
				"claim":     map[string]any{"type": "string"},
				"supported": map[string]any{"type": "boolean"},
				"evidence":  map[string]any{"type": "string"},
			}),
		}
	}
	return object(props)
}

// object builds a closed JSON Schema object requiring every property.
func object(props map[string]any) map[string]any {
	required := make([]string, 0, len(props))
	for k := range props {
		required = append(required, k)
	}
	sort.Strings(required)
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
