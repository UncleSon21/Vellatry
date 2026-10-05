package notebook

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/platform/db"
	"github.com/UncleSon21/vellatry/internal/testdb"
)

// What a session leaves behind. A note kept from an answer is a record of what was said
// and what it was said from, so it survives the source being deleted and cannot be
// quietly rewritten afterwards.
func TestNotesKeepWhatWasSaid(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "notes")

	tenant := func(fn func(ctx context.Context, tx pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(ctx, pool, org, fn); err != nil {
			t.Fatal(err)
		}
	}

	var nb Notebook
	var src Source
	var msg Message
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if nb, err = Create(ctx, tx, org, "Competitor research", "lead@example.com"); err != nil {
			return err
		}
		body := "Rival charges $89 a month for the Growth plan."
		if src, err = AddSource(ctx, tx, org, nb.ID, New{Kind: "text", Title: "Rival pricing", Body: body}); err != nil {
			return err
		}
		if err := SourceRead(ctx, tx, org, src.ID, "Rival pricing", body, Split(body)); err != nil {
			return err
		}
		if msg, err = Ask(ctx, tx, org, nb.ID, "lead@example.com", "What does Rival charge?"); err != nil {
			return err
		}
		return SaveAnswer(ctx, tx, msg.ID, "Rival charges $89 a month [1].", "answered",
			[]Citation{{Marker: 1, ChunkID: 1, Source: src.ID, Title: "Rival pricing", Seq: 0, Text: body}}, 0)
	})

	// A question still being answered is not a finding yet.
	var kept Note
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		waiting, err := Ask(ctx, tx, org, nb.ID, "lead@example.com", "And their support?")
		if err != nil {
			return err
		}
		if _, err := KeepAnswer(ctx, tx, org, waiting.ID, "", "lead@example.com"); err != ErrNotAnswered {
			t.Errorf("keeping an unanswered question = %v", err)
		}
		kept, err = KeepAnswer(ctx, tx, org, msg.ID, "", "lead@example.com")
		return err
	})
	if kept.Kind != "answer" || !kept.Frozen() {
		t.Fatalf("a kept answer = %+v", kept)
	}
	if kept.Title != "What does Rival charge?" {
		t.Errorf("a note with no title should be named after the question, got %q", kept.Title)
	}
	if len(kept.Citations) != 1 || kept.Citations[0].Text == "" {
		t.Errorf("a kept answer must carry the passage it was said from: %+v", kept.Citations)
	}

	// Deleting the source does not take the record with it: the passage was copied in,
	// not pointed at.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if err := DeleteSource(ctx, tx, src.ID); err != nil {
			return err
		}
		n, err := LoadNote(ctx, tx, kept.ID)
		if err != nil {
			return err
		}
		if n.Body != "Rival charges $89 a month [1]." || len(n.Citations) != 1 || !strings.Contains(n.Citations[0].Text, "$89") {
			t.Errorf("the note lost what it was based on when its source went: %+v", n)
		}
		return nil
	})

	// Its words cannot be rewritten; its name can.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := EditNote(ctx, tx, kept.ID, "", "Rival charges $49 a month."); err != ErrFrozen {
			t.Errorf("rewriting a kept answer = %v", err)
		}
		n, err := EditNote(ctx, tx, kept.ID, "Rival pricing, August", "")
		if err != nil {
			return err
		}
		if n.Title != "Rival pricing, August" || n.Body != kept.Body {
			t.Errorf("retitling changed more than the title: %+v", n)
		}
		return nil
	})

	// The team's own note is theirs to write and rewrite.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		own, err := WriteNote(ctx, tx, org, nb.ID, "", "Rival moved to annual billing in July. Worth watching.", "lead@example.com")
		if err != nil {
			return err
		}
		if own.Kind != "written" || own.Frozen() {
			t.Errorf("a written note = %+v", own)
		}
		if !strings.HasPrefix(own.Title, "Rival moved to annual billing") {
			t.Errorf("a note with no title should be named after its own words, got %q", own.Title)
		}
		edited, err := EditNote(ctx, tx, own.ID, "", "Rival moved to annual billing in July.")
		if err != nil || edited.Body != "Rival moved to annual billing in July." {
			t.Errorf("editing a written note = %+v %v", edited, err)
		}
		if _, err := WriteNote(ctx, tx, org, nb.ID, "", "   ", ""); err == nil {
			t.Error("an empty note was stored")
		}
		if _, err := WriteNote(ctx, tx, org, nb.ID, "", strings.Repeat("a", MaxNote+1), ""); err == nil {
			t.Error("a note longer than a source was stored")
		}
		list, err := Notes(ctx, tx, nb.ID, 10)
		if err != nil {
			return err
		}
		if len(list) != 2 || list[0].ID != own.ID {
			t.Errorf("notes = %d, newest first? %+v", len(list), list)
		}
		return DeleteNote(ctx, tx, own.ID)
	})

	// Notes are outputs, never inputs: nothing retrieves them, so an answer cannot be
	// grounded in an earlier answer.
	tenant(func(ctx context.Context, tx pgx.Tx) error {
		if _, err := WriteNote(ctx, tx, org, nb.ID, "Watch this", "Rival moved to annual billing in July.", ""); err != nil {
			return err
		}
		hits, err := ByText(ctx, tx, nb.ID, "annual billing July", Candidates)
		if err != nil {
			return err
		}
		if len(hits) != 0 {
			t.Errorf("a note was retrievable as a source: %+v", hits)
		}
		return nil
	})
}

// A recipe is a question worth asking again, of this notebook or the next one.
func TestRecipesAreKeptOncePerQuestion(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	org, _ := testdb.NewOrg(t, pool, "recipes")

	if err := db.InTenant(ctx, pool, org, func(ctx context.Context, tx pgx.Tx) error {
		first, err := SaveRecipe(ctx, tx, org, "", "What does this competitor charge, and how do they justify it?", "lead@example.com")
		if err != nil {
			return err
		}
		if first.Name == "" || first.Runs != 0 {
			t.Errorf("a new recipe = %+v", first)
		}
		// Saving the same question again is the same recipe, renamed.
		again, err := SaveRecipe(ctx, tx, org, "Competitor pricing", "What does this competitor charge, and how do they justify it?", "lead@example.com")
		if err != nil {
			return err
		}
		if again.ID != first.ID || again.Name != "Competitor pricing" {
			t.Errorf("the same question became a second recipe: %+v then %+v", first, again)
		}

		other, err := SaveRecipe(ctx, tx, org, "", "What do they claim about support?", "")
		if err != nil {
			return err
		}
		// Most used first: what a team asks again is what a team found useful.
		for i := 0; i < 3; i++ {
			if err := RecipeRun(ctx, tx, other.ID); err != nil {
				return err
			}
		}
		list, err := Recipes(ctx, tx, 10)
		if err != nil {
			return err
		}
		if len(list) != 2 || list[0].ID != other.ID || list[0].Runs != 3 || list[0].LastRunAt == nil {
			t.Errorf("recipes = %+v", list)
		}

		if _, err := SaveRecipe(ctx, tx, org, "", "  ", ""); err == nil {
			t.Error("an empty recipe was saved")
		}
		if _, err := SaveRecipe(ctx, tx, org, "", strings.Repeat("why ", 200), ""); err == nil {
			t.Error("a recipe longer than a question was saved")
		}
		if err := DeleteRecipe(ctx, tx, other.ID); err != nil {
			return err
		}
		if err := DeleteRecipe(ctx, tx, other.ID); err != ErrNotFound {
			t.Errorf("deleting a recipe twice = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Another organisation's recipes are not theirs to see or run.
	other, _ := testdb.NewOrg(t, pool, "recipes-other")
	if err := db.InTenant(ctx, pool, other, func(ctx context.Context, tx pgx.Tx) error {
		list, err := Recipes(ctx, tx, 10)
		if err != nil {
			return err
		}
		if len(list) != 0 {
			t.Errorf("another organisation saw %d recipes", len(list))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
