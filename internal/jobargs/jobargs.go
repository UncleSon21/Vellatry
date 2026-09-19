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
