package dataforseo

import (
	"context"
	"net/http"
	"testing"

	"github.com/UncleSon21/vellatry/internal/platform/budget"
	"github.com/UncleSon21/vellatry/internal/platform/halt"
)

func TestAccountFatalIsNarrow(t *testing.T) {
	fatal := []struct {
		status int
		msg    string
	}{
		{http.StatusUnauthorized, "Unauthorized."},
		{http.StatusForbidden, "Access denied."},
		{http.StatusPaymentRequired, "Payment Required."},
		{http.StatusOK, "Insufficient funds in the account."},
		{http.StatusOK, "Your balance is too low."},
	}
	for _, c := range fatal {
		if _, ok := accountFatal(c.status, c.msg); !ok {
			t.Errorf("%d %q should halt the worker: it means the account cannot be used", c.status, c.msg)
		}
	}

	// One call's problem, not the account's. Halting on these would stop every tenant's
	// work over one malformed request or one missing task.
	fine := []struct {
		status int
		msg    string
	}{
		{http.StatusOK, "Invalid Field: 'language_code'."},
		{http.StatusNotFound, "Task not found."},
		{http.StatusOK, "Task In Queue."},
		{http.StatusBadRequest, "Invalid Payload."},
	}
	for _, c := range fine {
		if _, ok := accountFatal(c.status, c.msg); ok {
			t.Errorf("%d %q halted the worker, but only that call failed", c.status, c.msg)
		}
	}
}

// An empty account arrives as a 200 with the real outcome in the envelope, which is the
// case most likely to be missed.
func TestAnEmptyAccountHaltsTheWorker(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status_code":40200,"status_message":"Insufficient funds in the account."}`))
	}, budget.New(1, 0.01))

	_, err := c.Live(context.Background(), Request{Engine: ChatGPT, Keyword: "anything", LocationCode: 2036, LanguageCode: "en"})
	f, ok := halt.IsFatal(err)
	if !ok {
		t.Fatalf("an empty account should halt the worker, got %v", err)
	}
	if f.Service != "dataforseo" {
		t.Errorf("service = %q", f.Service)
	}
}

// A bad request is this call's problem; halting would stop every tenant's work.
func TestABadRequestDoesNotHaltTheWorker(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status_code":40501,"status_message":"Invalid Field: 'language_code'."}`))
	}, budget.New(1, 0.01))

	_, err := c.Live(context.Background(), Request{Engine: ChatGPT, Keyword: "anything", LocationCode: 2036, LanguageCode: "en"})
	if err == nil {
		t.Fatal("the call should still fail")
	}
	if _, ok := halt.IsFatal(err); ok {
		t.Error("one malformed request halted the whole worker")
	}
}
