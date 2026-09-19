package promptgen

import "testing"

func TestFromTopic(t *testing.T) {
	got := FromTopic("  payroll   software ", "Australia")
	if len(got) != 4 {
		t.Fatalf("got %d prompts", len(got))
	}
	if got[0].Text != "What is the best payroll software in Australia?" || got[0].Source != "template" {
		t.Errorf("first = %+v", got[0])
	}
	if FromTopic(" ", "Australia") != nil {
		t.Error("empty topic should give no prompts")
	}
	if got := FromTopic("mattress", ""); got[0].Text != "What is the best mattress?" {
		t.Errorf("no location: %q", got[0].Text)
	}
}

func TestFromFanOut(t *testing.T) {
	got := FromFanOut(
		[]string{"best payroll app australia 2026", "payroll", "Best payroll app Australia 2026!", "xero payroll vs myob payroll", "known question here"},
		[]string{"Known question, here?"},
		5,
	)
	want := []string{"Best payroll app australia 2026?", "Xero payroll vs myob payroll?"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i].Text != want[i] || got[i].Source != "fan_out" {
			t.Errorf("got[%d] = %+v, want %q", i, got[i], want[i])
		}
	}
	if got := FromFanOut([]string{"one two three", "four five six"}, nil, 1); len(got) != 1 {
		t.Errorf("limit ignored: %+v", got)
	}
}

func TestKey(t *testing.T) {
	if Key("  Best  Payroll, app?? ") != "best payroll app" {
		t.Errorf("key = %q", Key("  Best  Payroll, app?? "))
	}
}
