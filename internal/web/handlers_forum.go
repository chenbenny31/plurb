package web

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chenbenny31/plurb/internal/rep"
	"github.com/chenbenny31/plurb/internal/store"
)

const (
	threadsPerPage    = 20
	threadsPerUser    = 10
	threadsWindow     = time.Hour
	commentsPerUser   = 30
	commentsWindow    = 10 * time.Minute
	votesPerUser      = 120
	votesWindow       = 10 * time.Minute
	maxThreadBodySize = 50_000
	maxCommentSize    = 20_000
)

// ---- thread list ----

type forumIndexPage struct {
	Threads  []*store.Thread
	Sort     string
	Page     int
	PrevPage int
	NextPage int
	Q        string
	Searched bool
}

func (s *Server) forumIndex(w http.ResponseWriter, r *http.Request) {
	sort := r.URL.Query().Get("sort")
	if sort != "new" && sort != "top" {
		sort = "hot"
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	// Fetch one extra row to know whether a next page exists.
	threads, err := s.store.ListThreads(r.Context(), sort, threadsPerPage+1, (page-1)*threadsPerPage)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := forumIndexPage{Sort: sort, Page: page}
	if len(threads) > threadsPerPage {
		threads, d.NextPage = threads[:threadsPerPage], page+1
	}
	if page > 1 {
		d.PrevPage = page - 1
	}
	d.Threads = threads
	v := s.newView(r, "Study Room", d)
	v.Section = "forum"
	s.render(w, r, http.StatusOK, "forum_index", v)
}

func (s *Server) forumSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		q = q[:200]
	}
	d := forumIndexPage{Q: q, Searched: true}
	if q != "" {
		var err error
		if d.Threads, err = s.store.SearchThreads(r.Context(), q, 50); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	v := s.newView(r, "Search the Study Room", d)
	v.Section, v.Q = "forum", q
	s.render(w, r, http.StatusOK, "forum_index", v)
}

// ---- new thread ----

type newThreadForm struct {
	Title, Body, Error string
}

func (s *Server) newThreadPage(w http.ResponseWriter, r *http.Request) {
	v := s.newView(r, "New thread", newThreadForm{})
	v.Section = "forum"
	s.render(w, r, http.StatusOK, "forum_new", v)
}

func (s *Server) createThread(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	u := currentUser(r)
	f := newThreadForm{
		Title: strings.TrimSpace(r.PostFormValue("title")),
		Body:  strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"),
	}
	switch n := utf8.RuneCountInString(f.Title); {
	case n == 0 || n > 200:
		f.Error = "Title must be 1–200 characters."
	case len(f.Body) > maxThreadBodySize:
		f.Error = "Thread body is too long (50 KB max)."
	}
	if f.Error != "" {
		v := s.newView(r, "New thread", f)
		v.Section = "forum"
		s.render(w, r, http.StatusUnprocessableEntity, "forum_new", v)
		return
	}
	if !s.allow(w, r, "thread:user:"+strconv.FormatInt(u.ID, 10), threadsPerUser, threadsWindow) {
		return
	}
	id, err := s.store.CreateThread(r.Context(), u.ID, f.Title, f.Body)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/forum/t/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// ---- thread page ----

type commentView struct {
	*store.Comment
	HTML   template.HTML
	MyVote int
}

type threadPage struct {
	Thread      *store.Thread
	Body        template.HTML
	MyVote      int
	Comments    []commentView
	CanDownvote bool
	ReplyTo     int64 // comment id when ?reply= is set, for no-JS replies
	Error       string
}

func (s *Server) threadByPath(w http.ResponseWriter, r *http.Request) (*store.Thread, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil, false
	}
	t, err := s.store.ThreadByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return t, true
}

func (s *Server) threadPage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.threadByPath(w, r)
	if !ok {
		return
	}
	s.renderThread(w, r, http.StatusOK, t, "")
}

func (s *Server) renderThread(w http.ResponseWriter, r *http.Request, status int, t *store.Thread, problem string) {
	ctx := r.Context()
	body, err := s.renderCached(ctx, "fmd:t:"+strconv.FormatInt(t.ID, 10), t.BodyMD)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	comments, err := s.store.CommentsByThread(ctx, t.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	votes := map[string]int{}
	u := currentUser(r)
	if u != nil {
		if votes, err = s.store.UserVotes(ctx, u.ID, t.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	d := threadPage{Thread: t, Body: body.HTML, MyVote: votes["thread:"+strconv.FormatInt(t.ID, 10)], Error: problem}
	d.CanDownvote = u != nil && u.Reputation >= rep.DownvoteMinRep
	d.ReplyTo, _ = strconv.ParseInt(r.URL.Query().Get("reply"), 10, 64)
	for _, c := range comments {
		out, err := s.renderCached(ctx, "fmd:c:"+strconv.FormatInt(c.ID, 10), c.BodyMD)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		d.Comments = append(d.Comments, commentView{Comment: c, HTML: out.HTML,
			MyVote: votes["comment:"+strconv.FormatInt(c.ID, 10)]})
	}
	v := s.newView(r, t.Title, d)
	v.Section = "forum"
	s.render(w, r, status, "forum_thread", v)
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	t, ok := s.threadByPath(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	body := strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"))
	parent, _ := strconv.ParseInt(r.PostFormValue("parent_id"), 10, 64)
	if body == "" || len(body) > maxCommentSize {
		s.renderThread(w, r, http.StatusUnprocessableEntity, t, "Comments must be 1–20,000 characters.")
		return
	}
	if !s.allow(w, r, "comment:user:"+strconv.FormatInt(u.ID, 10), commentsPerUser, commentsWindow) {
		return
	}
	id, err := s.store.CreateComment(r.Context(), t.ID, parent, u.ID, body)
	switch {
	case errors.Is(err, store.ErrCommentTooDeep):
		s.renderThread(w, r, http.StatusUnprocessableEntity, t, "That conversation is nested too deeply; reply further up.")
		return
	case errors.Is(err, store.ErrWrongThread), errors.Is(err, store.ErrNotFound):
		s.badRequest(w, r, "That comment doesn't exist in this thread.")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/forum/t/"+strconv.FormatInt(t.ID, 10)+"#c"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// ---- voting ----

type voteResponse struct {
	Score string `json:"score"`
	Value int    `json:"value"`
	Error string `json:"error,omitempty"`
}

// vote handles both plain form posts (redirect back) and fetch() calls
// from forum.js (JSON), so voting works with JavaScript off.
func (s *Server) vote(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	wantsJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	typ := r.PostFormValue("type")
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	value, _ := strconv.Atoi(r.PostFormValue("value"))
	thread, _ := strconv.ParseInt(r.PostFormValue("thread"), 10, 64)

	if !s.allow(w, r, "vote:user:"+strconv.FormatInt(u.ID, 10), votesPerUser, votesWindow) {
		return
	}
	res, err := s.store.Vote(r.Context(), u.ID, typ, id, value)
	var msg string
	switch {
	case errors.Is(err, store.ErrSelfVote), errors.Is(err, store.ErrDownvoteLocked):
		msg = err.Error()
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrBadVoteTarget):
		msg = "Nothing to vote on."
	case err != nil:
		s.serverError(w, r, err)
		return
	}

	if wantsJSON {
		w.Header().Set("Content-Type", "application/json")
		if msg != "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
		}
		json.NewEncoder(w).Encode(voteResponse{Score: formatScore(res.Score), Value: res.Value, Error: msg})
		return
	}
	if msg != "" {
		s.badRequest(w, r, msg)
		return
	}
	anchor := ""
	if typ == "comment" {
		anchor = "#c" + strconv.FormatInt(id, 10)
	}
	http.Redirect(w, r, "/forum/t/"+strconv.FormatInt(thread, 10)+anchor, http.StatusSeeOther)
}

// formatScore shows weighted scores compactly: 3, 2.6, -1.
func formatScore(f float64) string {
	return strconv.FormatFloat(float64(int64(f*10+sign(f)*0.5))/10, 'f', -1, 64)
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// ---- reputation history ----

type repPage struct {
	Events []store.RepEvent
}

func (s *Server) repHistory(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.RepHistory(r.Context(), currentUser(r).ID, 100)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.newView(r, "Reputation", repPage{events})
	v.Section = "forum"
	s.render(w, r, http.StatusOK, "rep_history", v)
}
