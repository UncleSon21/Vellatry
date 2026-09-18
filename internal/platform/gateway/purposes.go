package gateway

// DefaultPurposes is every reason Vellatry may call an LLM. Adding a line here is the
// only way to add an LLM call, so this list is the full inventory of LLM use.
//
// Everything that runs per answer, keyword or page is PerItem and refused by default.
// The judge is the one exception, allowed only during the design-partner phase through
// Config.AllowPerItem until the trained judge replaces it.
func DefaultPurposes() []Purpose {
	return []Purpose{
		// Per-item: refused unless explicitly allowed.
		{Name: "judge", PerItem: true, MaxInputChars: 12_000, MaxOutputTokens: 800, Tier: "cheap", EstimateUSD: 0.01},

		// User-triggered, metered as credits.
		{Name: "agent_plan", MaxInputChars: 40_000, MaxOutputTokens: 2_000, Tier: "strong", EstimateUSD: 0.10},
		{Name: "agent_narrate", MaxInputChars: 20_000, MaxOutputTokens: 1_500, Tier: "cheap", EstimateUSD: 0.02},
		{Name: "notebook_answer", MaxInputChars: 60_000, MaxOutputTokens: 2_000, Tier: "strong", EstimateUSD: 0.15},
		{Name: "report_draft", MaxInputChars: 30_000, MaxOutputTokens: 2_500, Tier: "strong", EstimateUSD: 0.10},
		{Name: "suggest_title_meta", MaxInputChars: 6_000, MaxOutputTokens: 400, Tier: "cheap", EstimateUSD: 0.005},
	}
}
