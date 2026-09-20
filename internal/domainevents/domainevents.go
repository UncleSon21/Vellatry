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

	NotificationCreated = "notification.created" // a watcher, the digest or a report raised something
	DestinationAdded    = "destination.added"
	DigestSent          = "digest.sent"
	TaskRequested       = "task.requested" // someone sent a fix or blindspot to Asana
	TaskCreated         = "task.created"
	TaskFailed          = "task.failed"
	TasksCompleted      = "task.completed" // Vellatry closed tasks whose item was resolved

	ReportDraftRequested = "report.draft_requested"
	ReportDrafted        = "report.drafted"
	ReportPublished      = "report.published"
	ReportWithdrawn      = "report.withdrawn"
	HubLoginRequested    = "hub.login_requested"
	KeywordRunRequested  = "keywords.run_requested"
	KeywordRunStarted    = "keywords.run_started"
	KeywordRunCompleted  = "keywords.run_completed"
	HubSignedIn          = "hub.signed_in"
)

// ReportPayload is the payload of the report events.
type ReportPayload struct {
	ReportID string `json:"report_id"`
	SeriesID string `json:"series_id,omitempty"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	Version  int    `json:"version,omitempty"`
	Notify   bool   `json:"notify,omitempty"` // email the recipients
}

// SiteCrawlCompletedPayload is the payload of SiteCrawlCompleted.
type SiteCrawlCompletedPayload struct {
	Pages          int      `json:"pages"`
	Opened         int      `json:"opened"`
	Resolved       int      `json:"resolved"`
	FixesLive      int      `json:"fixes_live"`
	CriticalOpened []string `json:"critical_opened,omitempty"` // fingerprints, for the critical_finding watcher
}

// NotificationPayload is the payload of NotificationCreated.
type NotificationPayload struct {
	NotificationID int64  `json:"notification_id"`
	Kind           string `json:"kind"`
	Severity       string `json:"severity"`
	Title          string `json:"title"`
	Delivery       string `json:"delivery"`
}

// TaskPayload is the payload of the task events.
type TaskPayload struct {
	Source    string `json:"source"`
	SubjectID string `json:"subject_id"`
	URL       string `json:"url,omitempty"`
	Error     string `json:"error,omitempty"`
}

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
			Name:  "authorize-connection",
			Kinds: []string{ConnectionAuthorized},
			Job: func(s events.Stored) river.JobArgs {
				switch connectionKind(s) {
				case "google":
					return jobargs.GoogleAuthorize{OrgID: s.OrgID}
				case "asana":
					return jobargs.AsanaAuthorize{OrgID: s.OrgID}
				}
				return nil
			},
		},
		{
			Name:  "revoke-connection",
			Kinds: []string{ConnectionRevoked},
			Job: func(s events.Stored) river.JobArgs {
				switch connectionKind(s) {
				case "google":
					return jobargs.GoogleRevoke{OrgID: s.OrgID}
				case "asana":
					return jobargs.AsanaRevoke{OrgID: s.OrgID}
				}
				return nil
			},
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
		{
			Name:  "watch-events",
			Kinds: []string{SiteCrawlCompleted, ConnectionBroken, BlindspotOpened},
			Job: func(s events.Stored) river.JobArgs {
				if s.Kind == BlindspotOpened {
					var p struct {
						Confirmed bool `json:"confirmed"`
					}
					if json.Unmarshal(s.Payload, &p) != nil || !p.Confirmed {
						return nil // provisional blindspots change too often to alert on
					}
				}
				if s.Kind == SiteCrawlCompleted {
					var p SiteCrawlCompletedPayload
					if json.Unmarshal(s.Payload, &p) != nil || len(p.CriticalOpened) == 0 {
						return nil
					}
				}
				return jobargs.WatchEvent{OrgID: s.OrgID, EventID: s.ID}
			},
		},
		{
			Name:  "deliver-notification",
			Kinds: []string{NotificationCreated},
			Job: func(s events.Stored) river.JobArgs {
				var p NotificationPayload
				if json.Unmarshal(s.Payload, &p) != nil || p.Delivery != "immediate" {
					return nil // digest items wait for the weekly digest
				}
				return jobargs.NotifyDeliver{OrgID: s.OrgID, NotificationID: p.NotificationID}
			},
		},
		{
			Name:  "test-destination",
			Kinds: []string{DestinationAdded},
			Job: func(s events.Stored) river.JobArgs {
				return jobargs.DestinationTest{OrgID: s.OrgID, DestinationID: s.SubjectID}
			},
		},
		{
			Name:  "create-task",
			Kinds: []string{TaskRequested},
			Job: func(s events.Stored) river.JobArgs {
				var p TaskPayload
				if json.Unmarshal(s.Payload, &p) != nil || p.Source == "" {
					return nil
				}
				return jobargs.AsanaCreateTask{OrgID: s.OrgID, Source: p.Source, SubjectID: p.SubjectID}
			},
		},
		{
			Name:  "draft-report",
			Kinds: []string{ReportDraftRequested},
			Job: func(s events.Stored) river.JobArgs {
				var p ReportPayload
				if json.Unmarshal(s.Payload, &p) != nil || p.SeriesID == "" {
					return nil
				}
				return jobargs.ReportDraft{OrgID: s.OrgID, SeriesID: p.SeriesID, Start: p.Start, End: p.End, Refresh: true}
			},
		},
		{
			Name:  "render-report-pdf",
			Kinds: []string{ReportPublished},
			Job: func(s events.Stored) river.JobArgs {
				var p ReportPayload
				if json.Unmarshal(s.Payload, &p) != nil || p.ReportID == "" {
					return nil
				}
				return jobargs.ReportPDF{OrgID: s.OrgID, ReportID: p.ReportID, Version: p.Version}
			},
		},
		{
			Name:  "announce-report",
			Kinds: []string{ReportPublished},
			Job: func(s events.Stored) river.JobArgs {
				var p ReportPayload
				if json.Unmarshal(s.Payload, &p) != nil || p.ReportID == "" {
					return nil
				}
				return jobargs.ReportNotify{OrgID: s.OrgID, ReportID: p.ReportID, Version: p.Version, Email: p.Notify}
			},
		},
		{
			Name:  "hub-sign-in-link",
			Kinds: []string{HubLoginRequested},
			Job: func(s events.Stored) river.JobArgs {
				var p struct {
					Email string `json:"email"`
				}
				if json.Unmarshal(s.Payload, &p) != nil || p.Email == "" {
					return nil
				}
				return jobargs.HubLoginEmail{OrgID: s.OrgID, Email: p.Email}
			},
		},
		{
			Name:  "research-keywords",
			Kinds: []string{KeywordRunRequested},
			Job: func(s events.Stored) river.JobArgs {
				var p struct {
					Seeds []string `json:"seeds"`
				}
				_ = json.Unmarshal(s.Payload, &p)
				return jobargs.KeywordRun{OrgID: s.OrgID, Trigger: "manual", Seeds: p.Seeds}
			},
		},
		{
			Name:  "first-keywords",
			Kinds: []string{OrgOnboarded},
			Job:   func(s events.Stored) river.JobArgs { return jobargs.KeywordRun{OrgID: s.OrgID, Trigger: "onboarding"} },
		},
		{
			Name:  "close-done-tasks",
			Kinds: []string{FixesLive, FixStatusChanged, BlindspotResolved, BlindspotStatusChanged},
			Job:   func(s events.Stored) river.JobArgs { return jobargs.AsanaCloseDone{OrgID: s.OrgID} },
		},
	}
}

func connectionKind(s events.Stored) string {
	var p ConnectionPayload
	_ = json.Unmarshal(s.Payload, &p)
	return p.Kind
}
