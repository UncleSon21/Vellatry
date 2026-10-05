package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/UncleSon21/vellatry/internal/domainevents"
	"github.com/UncleSon21/vellatry/internal/notebook"
	"github.com/UncleSon21/vellatry/internal/platform/events"
)

// The notebook's api does no reading, no fetching and no thinking: it records what the
// team added or asked and hands it to the worker. Every one of those three waits on
// something outside Vellatry, and the api never does.

func (s *Server) listNotebooks(w http.ResponseWriter, r *http.Request) {
	var out []notebook.Notebook
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = notebook.List(ctx, tx, limitParam(r, 50, 200))
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

func (s *Server) addNotebook(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	var out notebook.Notebook
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = notebook.Create(ctx, tx, sessionFrom(ctx).OrgID, in.Name, sessionFrom(ctx).UserID)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// notebookOut is a notebook with everything the page shows: one request opens it.
type notebookOut struct {
	notebook.Notebook
	SourceList []notebook.Source  `json:"sources_list"`
	Messages   []notebook.Message `json:"messages"`
}

func (s *Server) getNotebook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var out notebookOut
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if out.Notebook, err = notebook.Load(ctx, tx, id); err != nil {
			return err
		}
		if out.SourceList, err = notebook.Sources(ctx, tx, id); err != nil {
			return err
		}
		out.Messages, err = notebook.Messages(ctx, tx, id, 200)
		return err
	})
	if err != nil {
		s.fail(w, r, notebookError(err))
		return
	}
	out.SourceList, out.Messages = nonNilT(out.SourceList), nonNilT(out.Messages)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) patchNotebook(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		s.fail(w, r, badRequest("Give the notebook a name."))
		return
	}
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		return notebook.Rename(ctx, tx, r.PathValue("id"), in.Name)
	})
	if err != nil {
		s.fail(w, r, notebookError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteNotebook(w http.ResponseWriter, r *http.Request) {
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		return notebook.Delete(ctx, tx, r.PathValue("id"))
	})
	if err != nil {
		s.fail(w, r, notebookError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// addSource records a document and asks the worker to read it. A URL is only checked for
// shape here; whether it is safe to connect to is decided at connect time, by the same
// guard the site crawler uses, because a name can resolve anywhere.
func (s *Server) addSource(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind  string `json:"kind"`  // url | text | file
		URL   string `json:"url"`   // kind = url
		Title string `json:"title"` // optional for url, the file's name for file
		Text  string `json:"text"`  // kind = text or file
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	in.Kind, in.URL, in.Text = strings.TrimSpace(in.Kind), strings.TrimSpace(in.URL), strings.TrimSpace(in.Text)
	switch in.Kind {
	case "url":
		u, err := url.Parse(in.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			s.fail(w, r, badRequest("Give a web address starting with http:// or https://."))
			return
		}
		in.URL, in.Text = u.String(), ""
	case "text", "file":
		if in.Text == "" {
			s.fail(w, r, badRequest("There is no text to add."))
			return
		}
		if len(in.Text) > notebook.MaxSource {
			s.fail(w, r, badRequest("That is too long for one source. Split it and add the parts separately."))
			return
		}
		if in.Title == "" {
			in.Title = firstLine(in.Text)
		}
		in.URL = ""
	default:
		s.fail(w, r, badRequest("A source is a web address, pasted text or a text file."))
		return
	}

	id := r.PathValue("id")
	var out notebook.Source
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		org, user := sessionFrom(ctx).OrgID, sessionFrom(ctx).UserID
		var err error
		if out, err = notebook.AddSource(ctx, tx, org, id, notebook.New{
			Kind: in.Kind, Title: in.Title, URL: in.URL, Body: in.Text, By: user,
		}); err != nil {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.NotebookSourceAdded, SubjectID: out.ID, Actor: user,
			Payload: map[string]any{"notebook_id": id, "kind": in.Kind}})
		return err
	})
	if err != nil {
		s.fail(w, r, notebookError(err))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request) {
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		src, _, err := notebook.LoadSource(ctx, tx, r.PathValue("sourceId"))
		if err != nil {
			return err
		}
		if src.Notebook != r.PathValue("id") {
			return notebook.ErrNotFound
		}
		return notebook.DeleteSource(ctx, tx, src.ID)
	})
	if err != nil {
		s.fail(w, r, notebookError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// askNotebook records a question and hands it to the worker. The answer arrives on the
// event stream the dashboard is already following.
func (s *Server) askNotebook(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Question string `json:"question"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("id")
	var out notebook.Message
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		org, user := sessionFrom(ctx).OrgID, sessionFrom(ctx).UserID
		var err error
		if out, err = notebook.Ask(ctx, tx, org, id, user, in.Question); err != nil {
			return err
		}
		_, err = s.Bus.Emit(ctx, tx, org, events.Event{Kind: domainevents.NotebookQuestionAsked,
			SubjectID: strconv.FormatInt(out.ID, 10), Actor: user, Payload: map[string]any{"notebook_id": id}})
		return err
	})
	if err != nil {
		s.fail(w, r, notebookError(err))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) notebookMessages(w http.ResponseWriter, r *http.Request) {
	var out []notebook.Message
	err := s.tenant(r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = notebook.Messages(ctx, tx, r.PathValue("id"), limitParam(r, 200, 500))
		return err
	})
	if err != nil {
		s.fail(w, r, notebookError(err))
		return
	}
	writeJSON(w, http.StatusOK, nonNilT(out))
}

// notebookError turns the package's refusals into something the dashboard can show.
func notebookError(err error) error {
	switch {
	case errors.Is(err, notebook.ErrNotFound):
		return notFound("That notebook is gone.")
	case errors.Is(err, notebook.ErrAnswersWaiting):
		return conflict("You already have questions waiting on an answer here. They will appear in a moment.")
	case err != nil && strings.HasPrefix(err.Error(), "notebook: "):
		// Create, AddSource and Ask refuse an empty or oversize value by name.
		return badRequest(strings.ToUpper(err.Error()[10:11]) + err.Error()[11:] + ".")
	}
	return err
}

// firstLine names a pasted source after its own first line, which is what a person would
// have called it anyway.
func firstLine(text string) string {
	line := text
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#* "))
	if line == "" {
		return "Pasted text"
	}
	if r := []rune(line); len(r) > 80 {
		line = strings.TrimSpace(string(r[:80])) + "…"
	}
	return line
}
