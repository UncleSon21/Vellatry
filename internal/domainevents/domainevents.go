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
)

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
	}
}
