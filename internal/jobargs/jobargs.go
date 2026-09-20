// Package jobargs defines job arguments shared by the api role (which enqueues work)
// and the worker role (which runs it). It must stay free of external clients so the
// api can import it (internal/archtest enforces that).
package jobargs

import "github.com/riverqueue/river"

// VisibilityPlanAll fans planning out to every tenant with a brand.
type VisibilityPlanAll struct{}

func (VisibilityPlanAll) Kind() string { return "visibility_plan_all" }

// VisibilityPlanOrg plans and posts one tenant's answers within its daily budget.
type VisibilityPlanOrg struct {
	OrgID string `json:"org_id"`
}

func (VisibilityPlanOrg) Kind() string { return "visibility_plan_org" }

// VisibilityCollect fetches one queued task's answer once it is ready.
type VisibilityCollect struct {
	OrgID  string `json:"org_id"`
	TaskID int64  `json:"task_id"`
}

func (VisibilityCollect) Kind() string { return "visibility_collect" }

// VisibilityJudge scores one answer that mentions the brand.
type VisibilityJudge struct {
	OrgID    string `json:"org_id"`
	AnswerID int64  `json:"answer_id"`
}

func (VisibilityJudge) Kind() string { return "visibility_judge" }

// VisibilityCheckNow asks every engine one prompt right now, on the live lane.
type VisibilityCheckNow struct {
	OrgID    string `json:"org_id"`
	PromptID string `json:"prompt_id"`
}

func (VisibilityCheckNow) Kind() string { return "visibility_check_now" }

// GoogleAuthorize exchanges a stored authorisation code and lists the properties the
// grant can read.
type GoogleAuthorize struct {
	OrgID string `json:"org_id"`
}

func (GoogleAuthorize) Kind() string { return "google_authorize" }

// GoogleRevoke revokes the grant at Google and wipes the stored token.
type GoogleRevoke struct {
	OrgID string `json:"org_id"`
}

func (GoogleRevoke) Kind() string { return "google_revoke" }

// SyncAll fans daily syncs out to every connected tenant.
type SyncAll struct{}

func (SyncAll) Kind() string { return "sync_all" }

// SearchSyncOrg schedules one tenant's Search Console sync (backfill on first run).
type SearchSyncOrg struct {
	OrgID string `json:"org_id"`
}

func (SearchSyncOrg) Kind() string { return "search_sync_org" }

// SearchSyncRange syncs Search Console days [From, To] (YYYY-MM-DD) for one tenant.
type SearchSyncRange struct {
	OrgID string `json:"org_id"`
	From  string `json:"from"`
	To    string `json:"to"`
}

func (SearchSyncRange) Kind() string { return "search_sync_range" }

// AnalyticsSyncOrg schedules one tenant's GA4 sync (backfill on first run).
type AnalyticsSyncOrg struct {
	OrgID string `json:"org_id"`
}

func (AnalyticsSyncOrg) Kind() string { return "analytics_sync_org" }

// AnalyticsSyncRange syncs GA4 days [From, To] for one tenant.
type AnalyticsSyncRange struct {
	OrgID string `json:"org_id"`
	From  string `json:"from"`
	To    string `json:"to"`
}

func (AnalyticsSyncRange) Kind() string { return "analytics_sync_range" }

// SiteCrawlAll schedules the weekly crawl for every tenant with a brand.
type SiteCrawlAll struct{}

func (SiteCrawlAll) Kind() string { return "site_crawl_all" }

// SiteCrawl crawls and audits one tenant's site.
type SiteCrawl struct {
	OrgID   string `json:"org_id"`
	Trigger string `json:"trigger"` // schedule | manual | onboarding
}

func (SiteCrawl) Kind() string { return "site_crawl" }

// WatchEvent runs the watchers that react to one stored event.
type WatchEvent struct {
	OrgID   string `json:"org_id"`
	EventID int64  `json:"event_id"`
}

func (WatchEvent) Kind() string { return "watch_event" }

// WatchDailyAll fans the week-on-week watchers out to every tenant.
type WatchDailyAll struct{}

func (WatchDailyAll) Kind() string { return "watch_daily_all" }

// WatchDaily runs one tenant's week-on-week watchers for one day (YYYY-MM-DD).
type WatchDaily struct {
	OrgID string `json:"org_id"`
	Day   string `json:"day"`
}

func (WatchDaily) Kind() string { return "watch_daily" }

// NotifyDeliver sends one notification to its destinations.
type NotifyDeliver struct {
	OrgID          string `json:"org_id"`
	NotificationID int64  `json:"notification_id"`
}

func (NotifyDeliver) Kind() string { return "notify_deliver" }

// InsertOpts gives up on a delivery after about two hours of retries; the notification
// stays visible in the dashboard either way.
func (NotifyDeliver) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 8} }

// DigestAll fans the weekly digest out to every tenant.
type DigestAll struct{}

func (DigestAll) Kind() string { return "digest_all" }

// DigestOrg builds and sends one tenant's digest for the week ending the day before
// Week (a Monday, YYYY-MM-DD).
type DigestOrg struct {
	OrgID string `json:"org_id"`
	Week  string `json:"week"`
}

func (DigestOrg) Kind() string { return "digest_org" }

// DestinationTest sends a first message to a new destination to prove it works.
type DestinationTest struct {
	OrgID         string `json:"org_id"`
	DestinationID string `json:"destination_id"`
}

func (DestinationTest) Kind() string { return "destination_test" }

// AsanaAuthorize exchanges a stored Asana authorisation code and lists projects.
type AsanaAuthorize struct {
	OrgID string `json:"org_id"`
}

func (AsanaAuthorize) Kind() string { return "asana_authorize" }

// AsanaRevoke revokes the Asana grant and wipes the stored token.
type AsanaRevoke struct {
	OrgID string `json:"org_id"`
}

func (AsanaRevoke) Kind() string { return "asana_revoke" }

// AsanaCreateTask creates (or finds, or reopens) the Asana task for one fix or blindspot.
type AsanaCreateTask struct {
	OrgID     string `json:"org_id"`
	Source    string `json:"source"` // fix | blindspot
	SubjectID string `json:"subject_id"`
}

func (AsanaCreateTask) Kind() string { return "asana_create_task" }

// InsertOpts stops retrying after about two hours; the fix shows the failure.
func (AsanaCreateTask) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 8} }

// AsanaCloseDone completes the Asana tasks whose fix or blindspot is resolved.
type AsanaCloseDone struct {
	OrgID string `json:"org_id"`
}

func (AsanaCloseDone) Kind() string { return "asana_close_done" }

// ReportScheduleAll drafts every series whose latest period is complete and due.
type ReportScheduleAll struct{}

func (ReportScheduleAll) Kind() string { return "report_schedule_all" }

// ReportDraft builds (or, with Refresh, rebuilds) one report's draft.
type ReportDraft struct {
	OrgID    string `json:"org_id"`
	SeriesID string `json:"series_id"`
	Start    string `json:"start"` // YYYY-MM-DD
	End      string `json:"end"`
	Refresh  bool   `json:"refresh"`
}

func (ReportDraft) Kind() string { return "report_draft" }

// ReportPDF renders one published version as a PDF.
type ReportPDF struct {
	OrgID    string `json:"org_id"`
	ReportID string `json:"report_id"`
	Version  int    `json:"version"`
}

func (ReportPDF) Kind() string { return "report_pdf" }

// InsertOpts stops retrying a PDF after about an hour; the web view is unaffected.
func (ReportPDF) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 6} }

// ReportNotify tells the team and the recipients that a version was published.
type ReportNotify struct {
	OrgID    string `json:"org_id"`
	ReportID string `json:"report_id"`
	Version  int    `json:"version"`
	Email    bool   `json:"email"` // email the series' recipients
}

func (ReportNotify) Kind() string { return "report_notify" }

// InsertOpts: see NotifyDeliver.
func (ReportNotify) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 8} }

// HubLoginEmail emails a sign-in link to the reports hub.
type HubLoginEmail struct {
	OrgID string `json:"org_id"`
	Email string `json:"email"`
}

func (HubLoginEmail) Kind() string { return "hub_login_email" }

// InsertOpts: a sign-in link is only useful for minutes.
func (HubLoginEmail) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 3} }

// KeywordScheduleAll finds the tenants whose keyword research is due a refresh.
type KeywordScheduleAll struct{}

func (KeywordScheduleAll) Kind() string { return "keyword_schedule_all" }

// KeywordRun expands seeds into keywords and queues their search results.
type KeywordRun struct {
	OrgID   string   `json:"org_id"`
	Trigger string   `json:"trigger"` // manual | schedule | onboarding
	Seeds   []string `json:"seeds,omitempty"`
}

func (KeywordRun) Kind() string { return "keyword_run" }

// KeywordSERPCollect collects one keyword's search results.
type KeywordSERPCollect struct {
	OrgID  string `json:"org_id"`
	TaskID int64  `json:"task_id"`
}

func (KeywordSERPCollect) Kind() string { return "keyword_serp_collect" }

// KeywordCluster turns a run's search results into topics.
type KeywordCluster struct {
	OrgID string `json:"org_id"`
	RunID int64  `json:"run_id"`
}

func (KeywordCluster) Kind() string { return "keyword_cluster" }
