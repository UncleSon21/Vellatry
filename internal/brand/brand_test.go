package brand

import (
	"slices"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestSuggestionNeverOverwritesUserFields(t *testing.T) {
	b := Brand{Name: "Koala", Domain: "koala.com"}
	b, changed := Apply(b, Patch{Aliases: ptr([]string{"Koala Mattress"})}, ByUser)
	if !slices.Equal(changed, []string{"aliases"}) || b.Provenance["aliases"] != ByUser {
		t.Fatalf("user edit: changed %v, provenance %v", changed, b.Provenance)
	}

	b, changed = Apply(b, Patch{
		Aliases:    ptr([]string{"Koala Sleep"}),
		Exclusions: ptr([]string{"koala bear"}),
	}, BySuggested)
	if !slices.Equal(b.Aliases, []string{"Koala Mattress"}) {
		t.Errorf("suggestion overwrote user aliases: %v", b.Aliases)
	}
	if !slices.Equal(changed, []string{"exclusions"}) || b.Provenance["exclusions"] != BySuggested {
		t.Errorf("suggestion should fill the unset field only: changed %v, provenance %v", changed, b.Provenance)
	}

	// A user may still overwrite a suggestion.
	b, _ = Apply(b, Patch{Exclusions: ptr([]string{"koalas"})}, ByUser)
	if b.Provenance["exclusions"] != ByUser || !slices.Equal(b.Exclusions, []string{"koalas"}) {
		t.Errorf("user could not replace a suggestion: %+v", b)
	}
}

func TestApplyIgnoresNoOpsAndCleans(t *testing.T) {
	b := Brand{Name: "Koala", Domain: "koala.com", Aliases: []string{"Koala Mattress"}}
	_, changed := Apply(b, Patch{Name: ptr(" Koala "), Domain: ptr("https://www.koala.com/au"), Aliases: ptr([]string{"Koala  Mattress", "koala mattress", ""})}, ByUser)
	if len(changed) != 0 {
		t.Errorf("no-op patch changed %v", changed)
	}
}

func TestNormaliseDomain(t *testing.T) {
	for in, want := range map[string]string{
		"https://WWW.Koala.com/en-au?x=1": "koala.com",
		"shop.example.com.au/":            "shop.example.com.au",
		"  ":                              "",
	} {
		if got := NormaliseDomain(in); got != want {
			t.Errorf("NormaliseDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEntities(t *testing.T) {
	brand, comps := Entities(Brand{Name: "Koala", Domain: "koala.com", Exclusions: []string{"koala bear"}}, []Competitor{{Name: "Ecosa", Domains: []string{"ecosa.com.au"}}})
	if brand.Domains[0] != "koala.com" || brand.Exclusions[0] != "koala bear" || comps[0].Name != "Ecosa" {
		t.Errorf("entities = %+v %+v", brand, comps)
	}
}
