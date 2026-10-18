package agent

import (
	"strings"
	"testing"

	"github.com/UncleSon21/vellatry/internal/brand"
)

// Branded and non-branded clicks are two numbers a marketing team is judged on, and the
// split between them is decided here. Counting someone else's search as branded
// flatters the brand and understates the traffic the team's own work won, which is the
// error that matters in a product sold on honest measurement.
func TestBrandedSearchesAreMatchedOnWords(t *testing.T) {
	b := brand.Brand{Name: "Koala", Domain: "www.koala.com", Aliases: []string{"Koala Living", "Oak"}}
	tokens := brandTokens(b)

	for _, q := range []string{
		"koala",
		"koala mattress review",
		"is koala any good",
		"KOALA.COM returns",
		"koalas delivery time",  // a plural of the brand is still the brand
		"koala's trial period",  // and so is a possessive
		"koala living sofa bed", // an alias of more than one word
		"oak bed frame",         // a short alias, as its own word
	} {
		if !isBranded(q, tokens) {
			t.Errorf("%q was not counted as a branded search", q)
		}
	}
	for _, q := range []string{
		"best mattress australia",
		"ecosa vs emma",
		"oakley sunglasses",  // "oak" inside another word is not the brand
		"soaking a mattress", // nor is it in the middle of one
		"koalatee bedding",   // a different company whose name starts the same way
		"",
	} {
		if isBranded(q, tokens) {
			t.Errorf("%q was counted as a branded search", q)
		}
	}

	// The domain contributes its own first label, and nothing under three characters
	// becomes a token: a two-letter brand would match most of the language.
	short := brandTokens(brand.Brand{Name: "Oi", Domain: "oi.com.au", Aliases: []string{"a"}})
	if len(short) != 0 {
		t.Errorf("tokens from a two-letter brand = %v, want none", short)
	}
	if got := brandTokens(brand.Brand{Domain: "www.koala.com"}); len(got) != 1 || got[0] != "koala" {
		t.Errorf("tokens from a domain alone = %v", got)
	}

	// The split itself.
	month := map[string]int64{
		"koala mattress":          100,
		"best mattress australia": 60,
		"oakley sunglasses":       40,
	}
	branded, other := splitBranded(month, b)
	if branded != 100 || other != 100 {
		t.Errorf("split = %d branded and %d other, want 100 and 100", branded, other)
	}
}

// "Which searches moved most" has to include the ones that stopped: a search the team
// used to win and now does not is the most important mover there is, and the one a
// report is asked about.
func TestMoversIncludeWhatDisappeared(t *testing.T) {
	before := map[string]int64{"best mattress": 500, "koala review": 100, "steady search": 50}
	cur := map[string]int64{"koala review": 160, "steady search": 50, "new search": 20}

	got := movers(cur, before, 8)
	rows := map[string]string{}
	order := []string{}
	for _, r := range got.Rows {
		rows[r[0]] = r[2]
		order = append(order, r[0])
	}
	if _, ok := rows["best mattress"]; !ok {
		t.Errorf("a search that stopped entirely is not among the movers: %v", order)
	}
	if rows["best mattress"] != "-500" {
		t.Errorf("the lost search moved %q, want -500", rows["best mattress"])
	}
	if rows["koala review"] != "+60" || rows["new search"] != "+20" {
		t.Errorf("movers = %v", rows)
	}
	// A search that did not move is not a mover.
	if _, ok := rows["steady search"]; ok {
		t.Error("a search with no change was listed as a mover")
	}
	// Biggest move first, whichever direction.
	if len(order) != 3 || order[0] != "best mattress" || order[1] != "koala review" {
		t.Errorf("order = %v, want the largest move first", order)
	}
	// Clicks now is what it is now, which for a search that stopped is none.
	for _, r := range got.Rows {
		if r[0] == "best mattress" && r[1] != "0" {
			t.Errorf("a search that stopped shows %q clicks now", r[1])
		}
	}

	if limited := movers(cur, before, 2); len(limited.Rows) != 2 {
		t.Errorf("limit 2 returned %d rows", len(limited.Rows))
	}
	if none := movers(map[string]int64{}, map[string]int64{}, 8); len(none.Rows) != 0 {
		t.Errorf("nothing to compare = %v", none.Rows)
	}
	if !strings.Contains(got.Title, "moved") {
		t.Errorf("table title = %q", got.Title)
	}
}

func TestCountAgreesWithItsNoun(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{{0, "0 fixes"}, {1, "1 fix"}, {2, "2 fixes"}, {-1, "-1 fix"}, {1500, "1,500 fixes"}} {
		if got := Count(c.n, "fix", "fixes"); got != c.want {
			t.Errorf("Count(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}
