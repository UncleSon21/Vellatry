// Package brand stores what the customer tells us about their brand and competitors.
//
// Every field records who set it ("user" or "suggested"). Suggestions (from the site
// crawl or DataForSEO) only fill fields a user has not set: what a person wrote is
// never overwritten by a machine.
package brand

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/visibility/detect"
)

// Provenance values.
const (
	ByUser      = "user"
	BySuggested = "suggested"
)

// Brand is the org's brand.
type Brand struct {
	ID              string            `json:"id"`
	OrgID           string            `json:"org_id"`
	Name            string            `json:"name"`
	Domain          string            `json:"domain"`
	Aliases         []string          `json:"aliases"`
	Exclusions      []string          `json:"exclusions"`
	Differentiators []string          `json:"differentiators"`
	Provenance      map[string]string `json:"provenance"`
}

// Competitor is a brand the customer compares against.
type Competitor struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases"`
	Exclusions []string `json:"exclusions"`
	Domains    []string `json:"domains"`
	Source     string   `json:"source"`
}

// ErrNoBrand means the org has not set up its brand yet.
var ErrNoBrand = errors.New("brand: not set up")

// Patch is a partial update; nil fields are left unchanged.
type Patch struct {
	Name            *string   `json:"name,omitempty"`
	Domain          *string   `json:"domain,omitempty"`
	Aliases         *[]string `json:"aliases,omitempty"`
	Exclusions      *[]string `json:"exclusions,omitempty"`
	Differentiators *[]string `json:"differentiators,omitempty"`
}

// Apply returns b with p applied and each changed field marked with who changed it.
// A suggestion (by == BySuggested) never overwrites a field a user set.
func Apply(b Brand, p Patch, by string) (Brand, []string) {
	if b.Provenance == nil {
		b.Provenance = map[string]string{}
	} else {
		b.Provenance = maps(b.Provenance)
	}
	var changed []string
	set := func(field string, apply func()) {
		if by == BySuggested && b.Provenance[field] == ByUser {
			return
		}
		apply()
		b.Provenance[field] = by
		changed = append(changed, field)
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) != b.Name {
		set("name", func() { b.Name = strings.TrimSpace(*p.Name) })
	}
	if p.Domain != nil && NormaliseDomain(*p.Domain) != b.Domain {
		set("domain", func() { b.Domain = NormaliseDomain(*p.Domain) })
	}
	if p.Aliases != nil && !slices.Equal(Clean(*p.Aliases), b.Aliases) {
		set("aliases", func() { b.Aliases = Clean(*p.Aliases) })
	}
	if p.Exclusions != nil && !slices.Equal(Clean(*p.Exclusions), b.Exclusions) {
		set("exclusions", func() { b.Exclusions = Clean(*p.Exclusions) })
	}
	if p.Differentiators != nil && !slices.Equal(Clean(*p.Differentiators), b.Differentiators) {
		set("differentiators", func() { b.Differentiators = Clean(*p.Differentiators) })
	}
	return b, changed
}

// Entities converts the brand and competitors for the deterministic detector.
func Entities(b Brand, comps []Competitor) (detect.Entity, []detect.Entity) {
	brand := detect.Entity{Name: b.Name, Aliases: b.Aliases, Exclusions: b.Exclusions, Domains: []string{b.Domain}}
	out := make([]detect.Entity, len(comps))
	for i, c := range comps {
		out[i] = detect.Entity{Name: c.Name, Aliases: c.Aliases, Exclusions: c.Exclusions, Domains: c.Domains}
	}
	return brand, out
}

// Load returns the org's brand and competitors. tx must be scoped to the org.
func Load(ctx context.Context, tx pgx.Tx) (Brand, []Competitor, error) {
	var b Brand
	var prov []byte
	err := tx.QueryRow(ctx,
		`SELECT id::text, org_id::text, name, domain, aliases, exclusions, differentiators, provenance
		 FROM brands ORDER BY created_at LIMIT 1`).
		Scan(&b.ID, &b.OrgID, &b.Name, &b.Domain, &b.Aliases, &b.Exclusions, &b.Differentiators, &prov)
	if errors.Is(err, pgx.ErrNoRows) {
		return Brand{}, nil, ErrNoBrand
	}
	if err != nil {
		return Brand{}, nil, err
	}
	if err := json.Unmarshal(prov, &b.Provenance); err != nil {
		return Brand{}, nil, err
	}
	rows, err := tx.Query(ctx,
		`SELECT id::text, name, aliases, exclusions, domains, source FROM competitors
		 WHERE brand_id = $1 ORDER BY name`, b.ID)
	if err != nil {
		return Brand{}, nil, err
	}
	comps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Competitor, error) {
		var c Competitor
		err := r.Scan(&c.ID, &c.Name, &c.Aliases, &c.Exclusions, &c.Domains, &c.Source)
		return c, err
	})
	return b, comps, err
}

// Save writes the brand's editable fields and provenance.
func Save(ctx context.Context, tx pgx.Tx, b Brand) error {
	prov, err := json.Marshal(b.Provenance)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE brands SET name = $2, domain = $3, aliases = $4, exclusions = $5, differentiators = $6, provenance = $7
		 WHERE id = $1`,
		b.ID, b.Name, b.Domain, b.Aliases, b.Exclusions, b.Differentiators, prov)
	return err
}

// AddCompetitor inserts a competitor for the brand.
func AddCompetitor(ctx context.Context, tx pgx.Tx, orgID, brandID string, c Competitor) (Competitor, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return Competitor{}, errors.New("brand: competitor needs a name")
	}
	if c.Source == "" {
		c.Source = ByUser
	}
	c.Aliases, c.Exclusions = Clean(c.Aliases), Clean(c.Exclusions)
	domains := make([]string, 0, len(c.Domains))
	for _, d := range c.Domains {
		if d = NormaliseDomain(d); d != "" {
			domains = append(domains, d)
		}
	}
	c.Domains = domains
	err := tx.QueryRow(ctx,
		`INSERT INTO competitors (org_id, brand_id, name, aliases, exclusions, domains, source)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
		orgID, brandID, c.Name, c.Aliases, c.Exclusions, c.Domains, c.Source).Scan(&c.ID)
	return c, err
}

// DeleteCompetitor removes one competitor.
func DeleteCompetitor(ctx context.Context, tx pgx.Tx, id string) error {
	tag, err := tx.Exec(ctx, "DELETE FROM competitors WHERE id = $1", id)
	if err == nil && tag.RowsAffected() == 0 {
		return fmt.Errorf("brand: competitor %s not found", id)
	}
	return err
}

// NormaliseDomain reduces a URL or host to a bare lower-case host without "www.".
func NormaliseDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "www.")
}

// Clean trims, drops empties and duplicates (case-insensitive), keeping order.
func Clean(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range list {
		s = strings.Join(strings.Fields(s), " ")
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}

func maps(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
