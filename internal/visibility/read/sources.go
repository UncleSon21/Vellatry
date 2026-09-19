package read

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SourceType classifies a cited domain.
type SourceType string

const (
	Owned      SourceType = "owned"
	Competitor SourceType = "competitor"
	UGC        SourceType = "ugc"       // forums, social, video
	Review     SourceType = "review"    // review and comparison sites
	Reference  SourceType = "reference" // encyclopaedias, government, education
	Editorial  SourceType = "editorial" // everything else: publishers, blogs
)

var ugcDomains = []string{"reddit.com", "quora.com", "youtube.com", "facebook.com", "x.com", "twitter.com", "tiktok.com", "linkedin.com", "instagram.com", "medium.com", "whirlpool.net.au"}
var reviewDomains = []string{"productreview.com.au", "trustpilot.com", "g2.com", "capterra.com", "capterra.com.au", "canstar.com.au", "canstarblue.com.au", "choice.com.au", "finder.com.au", "getapp.com"}
var referenceDomains = []string{"wikipedia.org", "gov.au", "edu.au", "gov", "edu", "abs.gov.au", "ato.gov.au"}

// Classify types a domain given the brand's and competitors' domains.
func Classify(domain, brandDomain string, competitorDomains []string) SourceType {
	d := strings.TrimPrefix(strings.ToLower(domain), "www.")
	switch {
	case under(d, brandDomain):
		return Owned
	case anyUnder(d, competitorDomains):
		return Competitor
	case anyUnder(d, reviewDomains):
		return Review
	case anyUnder(d, ugcDomains):
		return UGC
	case anyUnder(d, referenceDomains):
		return Reference
	}
	return Editorial
}

func under(d, parent string) bool {
	parent = strings.TrimPrefix(strings.ToLower(parent), "www.")
	return parent != "" && (d == parent || strings.HasSuffix(d, "."+parent))
}

func anyUnder(d string, parents []string) bool {
	for _, p := range parents {
		if under(d, p) {
			return true
		}
	}
	return false
}

// SourceRow is one cited domain over the period.
type SourceRow struct {
	Domain    string         `json:"domain"`
	Type      SourceType     `json:"type"`
	Citations int            `json:"citations"`
	ByEngine  map[string]int `json:"by_engine"`
}

// LoadSources returns cited domains for [from, to], most cited first.
func LoadSources(ctx context.Context, tx pgx.Tx, from, to time.Time, brandDomain string, competitorDomains []string, limit int) ([]SourceRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT domain, engine, sum(citations)::int FROM source_citations_daily
		WHERE day BETWEEN $1::date AND $2::date GROUP BY domain, engine`, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	byDomain := map[string]*SourceRow{}
	for rows.Next() {
		var domain, eng string
		var n int
		if err := rows.Scan(&domain, &eng, &n); err != nil {
			return nil, err
		}
		r := byDomain[domain]
		if r == nil {
			r = &SourceRow{Domain: domain, Type: Classify(domain, brandDomain, competitorDomains), ByEngine: map[string]int{}}
			byDomain[domain] = r
		}
		r.Citations += n
		r.ByEngine[eng] += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SourceRow, 0, len(byDomain))
	for _, r := range byDomain {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Citations != out[j].Citations {
			return out[i].Citations > out[j].Citations
		}
		return out[i].Domain < out[j].Domain
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
