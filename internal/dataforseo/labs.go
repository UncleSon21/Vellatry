package dataforseo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Keyword research uses DataForSEO Labs (keyword ideas, competitors' ranking keywords)
// and the standard SERP queue (the top ten organic results per keyword, which is what
// clusters keywords together). No LLM is involved anywhere in this path.

// KeywordData is one keyword with its demand.
type KeywordData struct {
	Keyword      string        `json:"keyword"`
	SearchVolume int           `json:"search_volume"`
	CPC          *float64      `json:"cpc"`
	Competition  *float64      `json:"competition"`
	Difficulty   *int          `json:"difficulty"` // 0-100
	Intent       string        `json:"intent"`     // informational | navigational | commercial | transactional
	Monthly      []MonthVolume `json:"monthly"`
}

// MonthVolume is one month of search volume.
type MonthVolume struct {
	Year   int `json:"year"`
	Month  int `json:"month"`
	Volume int `json:"volume"`
}

// labsItem is the shape Labs endpoints share.
type labsItem struct {
	Keyword     string `json:"keyword"`
	KeywordInfo struct {
		SearchVolume    *int     `json:"search_volume"`
		CPC             *float64 `json:"cpc"`
		Competition     *float64 `json:"competition"`
		MonthlySearches []struct {
			Year         int  `json:"year"`
			Month        int  `json:"month"`
			SearchVolume *int `json:"search_volume"`
		} `json:"monthly_searches"`
	} `json:"keyword_info"`
	KeywordProperties struct {
		KeywordDifficulty *int `json:"keyword_difficulty"`
	} `json:"keyword_properties"`
	SearchIntentInfo struct {
		MainIntent string `json:"main_intent"`
	} `json:"search_intent_info"`
}

func (i labsItem) data() KeywordData {
	k := KeywordData{Keyword: i.Keyword, CPC: i.KeywordInfo.CPC, Competition: i.KeywordInfo.Competition,
		Difficulty: i.KeywordProperties.KeywordDifficulty, Intent: i.SearchIntentInfo.MainIntent}
	if i.KeywordInfo.SearchVolume != nil {
		k.SearchVolume = *i.KeywordInfo.SearchVolume
	}
	for _, m := range i.KeywordInfo.MonthlySearches {
		v := 0
		if m.SearchVolume != nil {
			v = *m.SearchVolume
		}
		k.Monthly = append(k.Monthly, MonthVolume{Year: m.Year, Month: m.Month, Volume: v})
	}
	return k
}

// labsUnits converts a Labs request into budget units of one standard SERP task, so a
// budget shared with SERP tasks reserves something close to the real cost (a Labs call
// is roughly a cent plus a fraction of a cent per item returned).
func labsUnits(limit int) int { return 20 + limit/5 }

// firstResult decodes the single result object Labs endpoints return.
func firstResult(env *envelope, v any) error {
	if len(env.Tasks) == 0 {
		return &APIError{Message: "no task in response"}
	}
	t := env.Tasks[0]
	if t.StatusCode != 20000 {
		return &APIError{StatusCode: t.StatusCode, Message: t.StatusMessage, Transient: t.StatusCode >= 50000}
	}
	var results []json.RawMessage
	if len(t.Result) > 0 && string(t.Result) != "null" {
		if err := json.Unmarshal(t.Result, &results); err != nil {
			return &APIError{Message: "decode result: " + err.Error()}
		}
	}
	if len(results) == 0 {
		return nil
	}
	if err := json.Unmarshal(results[0], v); err != nil {
		return &APIError{Message: "decode result: " + err.Error()}
	}
	return nil
}

// KeywordIdeas returns keywords Google associates with the seeds.
func (c *Client) KeywordIdeas(ctx context.Context, seeds []string, locationCode int, language string, limit int) ([]KeywordData, error) {
	if len(seeds) == 0 {
		return nil, nil
	}
	if len(seeds) > 200 {
		seeds = seeds[:200]
	}
	body := []map[string]any{{
		"keywords": seeds, "location_code": locationCode, "language_code": language,
		"limit": limit, "order_by": []string{"keyword_info.search_volume,desc"},
	}}
	env, err := c.call(ctx, http.MethodPost, "/v3/dataforseo_labs/google/keyword_ideas/live", body, labsUnits(limit))
	if err != nil {
		return nil, err
	}
	var result struct {
		Items []labsItem `json:"items"`
	}
	if err := firstResult(env, &result); err != nil {
		return nil, err
	}
	out := make([]KeywordData, 0, len(result.Items))
	for _, i := range result.Items {
		if i.Keyword != "" {
			out = append(out, i.data())
		}
	}
	return out, nil
}

// RankedKeyword is a keyword a domain ranks for.
type RankedKeyword struct {
	KeywordData
	Rank int    `json:"rank"` // absolute position
	URL  string `json:"url"`
}

// RankedKeywords returns the keywords a domain ranks for: the competitor gap.
func (c *Client) RankedKeywords(ctx context.Context, domain string, locationCode int, language string, limit int) ([]RankedKeyword, error) {
	domain = strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")
	domain = strings.TrimPrefix(domain, "www.")
	if domain == "" {
		return nil, nil
	}
	body := []map[string]any{{
		"target": domain, "location_code": locationCode, "language_code": language, "limit": limit,
		"order_by": []string{"keyword_data.keyword_info.search_volume,desc"},
	}}
	env, err := c.call(ctx, http.MethodPost, "/v3/dataforseo_labs/google/ranked_keywords/live", body, labsUnits(limit))
	if err != nil {
		return nil, err
	}
	var result struct {
		Items []struct {
			KeywordData       labsItem `json:"keyword_data"`
			RankedSERPElement struct {
				SERPItem struct {
					RankAbsolute int    `json:"rank_absolute"`
					URL          string `json:"url"`
				} `json:"serp_item"`
			} `json:"ranked_serp_element"`
		} `json:"items"`
	}
	if err := firstResult(env, &result); err != nil {
		return nil, err
	}
	out := make([]RankedKeyword, 0, len(result.Items))
	for _, i := range result.Items {
		if i.KeywordData.Keyword == "" {
			continue
		}
		out = append(out, RankedKeyword{KeywordData: i.KeywordData.data(),
			Rank: i.RankedSERPElement.SERPItem.RankAbsolute, URL: i.RankedSERPElement.SERPItem.URL})
	}
	return out, nil
}

// ---- organic SERPs (the clustering signal) --------------------------------------------

const organicPost = "/v3/serp/google/organic/task_post"
const organicGet = "/v3/serp/google/organic/task_get/regular/"

// OrganicRequest asks for one keyword's search results.
type OrganicRequest struct {
	Keyword      string
	LocationCode int
	LanguageCode string
	Tag          string
}

// OrganicSERP is what came back for one keyword.
type OrganicSERP struct {
	TaskID   string
	Tag      string
	Keyword  string
	URLs     []string // organic results, in order
	Features []string // the other result types present: shopping, local_pack, people_also_ask, ai_overview...
	Cost     float64
}

// PostOrganic queues up to 100 keywords on the standard queue.
func (c *Client) PostOrganic(ctx context.Context, reqs []OrganicRequest) ([]Posted, error) {
	if len(reqs) == 0 || len(reqs) > 100 {
		return nil, fmt.Errorf("dataforseo: post 1 to 100 SERP tasks, got %d", len(reqs))
	}
	bodies := make([]map[string]any, len(reqs))
	for i, r := range reqs {
		bodies[i] = map[string]any{
			"keyword": r.Keyword, "location_code": r.LocationCode, "language_code": r.LanguageCode,
			"device": "desktop", "depth": 10,
		}
		if r.Tag != "" {
			bodies[i]["tag"] = r.Tag
		}
	}
	env, err := c.call(ctx, http.MethodPost, organicPost, bodies, len(reqs))
	if err != nil {
		return nil, err
	}
	out := make([]Posted, len(reqs))
	for i := range reqs {
		out[i].Tag = reqs[i].Tag
		if i >= len(env.Tasks) {
			out[i].Err = &APIError{Message: "task missing from response"}
			continue
		}
		t := env.Tasks[i]
		out[i].TaskID, out[i].Cost = t.ID, t.Cost
		if t.StatusCode != 20100 && t.StatusCode != 20000 {
			out[i].Err = &APIError{StatusCode: t.StatusCode, Message: t.StatusMessage, Transient: t.StatusCode >= 50000}
		}
	}
	return out, nil
}

// GetOrganic fetches a queued SERP. ready is false while it is still in the queue.
func (c *Client) GetOrganic(ctx context.Context, taskID string) (OrganicSERP, bool, error) {
	env, err := c.call(ctx, http.MethodGet, organicGet+url.PathEscape(taskID), nil, 0)
	if err != nil {
		return OrganicSERP{}, false, err
	}
	if len(env.Tasks) != 1 {
		return OrganicSERP{}, false, &APIError{Message: fmt.Sprintf("expected 1 task, got %d", len(env.Tasks))}
	}
	t := env.Tasks[0]
	switch t.StatusCode {
	case 40601, 40602: // handed to a worker / still in queue
		return OrganicSERP{}, false, nil
	}
	if t.StatusCode != 20000 {
		return OrganicSERP{}, false, &APIError{StatusCode: t.StatusCode, Message: t.StatusMessage, Transient: t.StatusCode >= 50000}
	}
	s := OrganicSERP{TaskID: t.ID, Cost: t.Cost}
	var data struct {
		Tag     string `json:"tag"`
		Keyword string `json:"keyword"`
	}
	_ = json.Unmarshal(t.Data, &data)
	s.Tag, s.Keyword = data.Tag, data.Keyword

	var result struct {
		Keyword   string   `json:"keyword"`
		ItemTypes []string `json:"item_types"`
		Items     []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"items"`
	}
	if err := firstResult(env, &result); err != nil {
		return s, false, err
	}
	if result.Keyword != "" {
		s.Keyword = result.Keyword
	}
	for _, it := range result.Items {
		if it.Type == "organic" && it.URL != "" && len(s.URLs) < 10 {
			s.URLs = append(s.URLs, it.URL)
		}
	}
	for _, f := range result.ItemTypes {
		if f != "organic" {
			s.Features = append(s.Features, f)
		}
	}
	return s, true, nil
}
