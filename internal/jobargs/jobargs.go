// Package jobargs defines job arguments shared by the api role (which enqueues work)
// and the worker role (which runs it). It must stay free of external clients so the
// api can import it (internal/archtest enforces that).
package jobargs

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
