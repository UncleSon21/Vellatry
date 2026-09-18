// Package events records domain events and hands them to subscribers.
//
// Emit writes the event row and one job per matching subscription in the caller's
// transaction. If the transaction commits, every subscriber runs; if it rolls back,
// neither the event nor its jobs exist. There is no outbox poller: the job queue is
// the outbox. The events table doubles as the change timeline, audit log and learning
// ledger.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/UncleSon21/vellatry/internal/platform/db"
)

// Event is something that happened to a tenant.
type Event struct {
	Kind      string // e.g. "visibility.snapshot.created"
	SubjectID string
	Actor     string // user id, "system" or a job kind
	Payload   any
}

// Stored is an event as persisted.
type Stored struct {
	ID         int64           `json:"id"`
	OrgID      string          `json:"org_id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Kind       string          `json:"kind"`
	SubjectID  string          `json:"subject_id,omitempty"`
	Actor      string          `json:"actor,omitempty"`
	Payload    json.RawMessage `json:"payload"`
}

// Subscription turns matching events into a job. Subscriber jobs must be idempotent.
type Subscription struct {
	Name  string
	Kinds []string // exact kinds, or "*" for every event
	Job   func(Stored) river.JobArgs
}

// Inserter is the part of the River client Emit needs.
type Inserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// Bus emits events to its subscriptions.
type Bus struct {
	jobs Inserter
	subs []Subscription
}

// NewBus returns a bus that enqueues subscriber jobs through jobs.
func NewBus(jobs Inserter, subs ...Subscription) *Bus { return &Bus{jobs: jobs, subs: subs} }

// Emit records e for orgID and enqueues its subscribers, all inside tx. tx must come from
// db.InTenant for orgID (row-level security checks the org) or db.InSystem.
func (b *Bus) Emit(ctx context.Context, tx pgx.Tx, orgID string, e Event) (Stored, error) {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return Stored{}, fmt.Errorf("events: payload: %w", err)
	}
	if e.Payload == nil {
		payload = []byte("{}")
	}
	s := Stored{OrgID: orgID, Kind: e.Kind, SubjectID: e.SubjectID, Actor: e.Actor, Payload: payload}
	err = tx.QueryRow(ctx,
		`INSERT INTO events (org_id, kind, subject_id, actor, payload)
		 VALUES ($1, $2, nullif($3, ''), nullif($4, ''), $5)
		 RETURNING id, occurred_at`,
		orgID, e.Kind, e.SubjectID, e.Actor, payload,
	).Scan(&s.ID, &s.OccurredAt)
	if err != nil {
		return Stored{}, fmt.Errorf("events: insert %s: %w", e.Kind, err)
	}

	var jobs []river.JobArgs
	for _, sub := range b.subs {
		if matches(sub.Kinds, e.Kind) {
			jobs = append(jobs, sub.Job(s))
		}
	}
	if len(jobs) == 0 {
		return s, nil
	}
	// The queue's tables have no row-level security and are not tenant data.
	err = db.AsOwner(ctx, tx, func() error {
		for _, j := range jobs {
			if _, err := b.jobs.InsertTx(ctx, tx, j, nil); err != nil {
				return fmt.Errorf("events: enqueue %s for %s: %w", j.Kind(), e.Kind, err)
			}
		}
		return nil
	})
	return s, err
}

func matches(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == "*" || k == kind {
			return true
		}
	}
	return false
}
