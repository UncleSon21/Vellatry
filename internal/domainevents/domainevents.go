// Package domainevents names every event kind, its payload, and which jobs subscribe to
// it. Both roles build their event bus from Subscriptions(), so an event triggers the
// same work whichever role emitted it. It must stay free of external clients.
package domainevents

import (
	"encoding/json"

	"github.com/riverqueue/river"

	"github.com/UncleSon21/vellatry/internal/jobargs"
	"github.com/UncleSon21/vellatry/internal/platform/events"
)

// Event kinds.
const (
	OrgOnboarded           = "org.onboarded"
	BrandUpdated           = "brand.updated"
	PromptStatusChanged    = "prompt.status_changed"
	TopicAdded             = "topic.added"
	AnswerCollected        = "visibility.answer.collected"
	AnswerJudged           = "visibility.answer.judged"
	BlindspotOpened        = "visibility.blindspot.opened"
	BlindspotResolved      = "visibility.blindspot.resolved"
	BlindspotStatusChanged = "visibility.blindspot.status_changed"
	CheckRequested         = "visibility.check.requested"
	CheckSkipped           = "visibility.check.skipped"

	ConnectionAuthorized      = "connection.authorized"       // a Google consent returned a code
	ConnectionPropertiesReady = "connection.properties_ready" // properties listed; the user can pick one
	ConnectionConnected       = "connection.connected"        // a property was chosen; payload {kind}
	ConnectionBroken          = "connection.broken"           // shown openly in the dashboard with a fix
	ConnectionRevoked         = "connection.revoked"
	SearchSynced              = "search.synced"
	AnalyticsSynced           = "analytics.synced"
	ReconciliationFailed      = "search.reconciliation_failed"

	SiteCrawlRequested   = "site.crawl.requested"
	SiteCrawlProgress    = "site.crawl.progress"
	SiteCrawlCompleted   = "site.crawl.completed"
	SiteCrawlFailed      = "site.crawl.failed"
	FindingStatusChanged = "site.finding.status_changed"
	FixStatusChanged     = "fix.status_changed"
	FixesLive            = "fix.live" // fixes detected live on the site
)

// ConnectionPayload is the payload of connection events.
type ConnectionPayload struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// AnswerCollectedPayload is the payload of AnswerCollected.
type AnswerCollectedPayload struct {
	AnswerID  int64  `json:"answer_id"`
	PromptID  string `json:"prompt_id"`
	Engine    string `json:"engine"`
	Mentioned bool   `json:"mentioned"`
	Band      string `json:"band,omitempty"`
}

// Subscriptions maps events to the jobs they trigger.
func Subscriptions() []events.Subscription {
	return []events.Subscription{
		{
			Name:  "judge-mentioned-answers",
			Kinds: []string{AnswerCollected},
			Job: func(s events.Stored) river.JobArgs {
				var p AnswerCollectedPayload
				if json.Unmarshal(s.Payload, &p) != nil || !p.Mentioned {
					return nil // the judge only runs on answers that mention the brand
				}
				return jobargs.VisibilityJudge{OrgID: s.OrgID, AnswerID: p.AnswerID}
			},
		},
		{
			Name:  "plan-after-setup-change",
			Kinds: []string{OrgOnboarded, TopicAdded},
			Job: func(s events.Stored) river.JobArgs {
				return jobargs.VisibilityPlanOrg{OrgID: s.OrgID} // start discovery now, not at the next quarter hour
			},
		},
		{
			Name:  "check-now",
			Kinds: []string{CheckRequested},
			Job: func(s events.Stored) river.JobArgs {
				return jobargs.VisibilityCheckNow{OrgID: s.OrgID, PromptID: s.SubjectID}
			},
		},
		{
			Name:  "first-crawl",
			Kinds: []string{OrgOnboarded},
			Job:   func(s events.Stored) river.JobArgs { return jobargs.SiteCrawl{OrgID: s.OrgID, Trigger: "onboarding"} },
		},
		{
			Name:  "crawl-now",
			Kinds: []string{SiteCrawlRequested},
			Job:   func(s events.Stored) river.JobArgs { return jobargs.SiteCrawl{OrgID: s.OrgID, Trigger: "manual"} },
		},
		{
			Name:  "google-authorize",
			Kinds: []string{ConnectionAuthorized},
			Job:   func(s events.Stored) river.JobArgs { return jobargs.GoogleAuthorize{OrgID: s.OrgID} },
		},
		{
			Name:  "google-revoke",
			Kinds: []string{ConnectionRevoked},
			Job:   func(s events.Stored) river.JobArgs { return jobargs.GoogleRevoke{OrgID: s.OrgID} },
		},
		{
			Name:  "first-sync",
			Kinds: []string{ConnectionConnected},
			Job: func(s events.Stored) river.JobArgs {
				var p ConnectionPayload
				_ = json.Unmarshal(s.Payload, &p)
				switch p.Kind {
				case "search_console":
					return jobargs.SearchSyncOrg{OrgID: s.OrgID}
				case "ga4":
					return jobargs.AnalyticsSyncOrg{OrgID: s.OrgID}
				}
				return nil
			},
		},
	}
}
