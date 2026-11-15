package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/chenbenny31/plurb/internal/auth"
	"github.com/chenbenny31/plurb/internal/store"
)

const (
	maxBodyBytes = 200_000
	maxTags      = 10
	// Form bodies are text only; cap them well above the markdown limit.
	maxFormBytes = 512 << 10
)

// ---- dashboard ----

type dashboardPage struct {
	Posts  []*store.Post
	Series []store.Series
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	posts, err := s.store.PostsByAuthor(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	series, err := s.store.SeriesByAuthor(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboard", s.newView(r, "Dashboard", dashboardPage{posts, series}))
}

// ---- post editor ----

type editPage struct {
	PostID     int64 // 0 = new post
	Status     string
	Saved      bool
	Form       postForm
	Error      string
	AuthorTags []store.Tag
	Series     []store.Series
}

// postForm holds raw form values so a failed save re-renders what was typed.
type postForm struct {
	Title, Slug, Description, CoverURL, Body, Tags, SeriesTitle, SeriesDescription string
}

func formFromPost(p *store.Post, seriesTitle string) postForm {
	names := make([]string, len(p.Tags))
	for i, t := range p.Tags {
		names[i] = t.Name
	}
	return postForm{
		Title: p.Title, Slug: p.Slug, Description: p.Description, CoverURL: p.CoverURL,
		Body: p.BodyMD, Tags: strings.Join(names, " "), SeriesTitle: seriesTitle,
	}
}

func readPostForm(r *http.Request) postForm {
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	return postForm{
		Title: f("title"), Slug: strings.ToLower(f("slug")), Description: f("description"),
		CoverURL: f("cover_url"), Body: r.PostFormValue("body"), Tags: f("tags"),
		SeriesTitle: f("series"), SeriesDescription: f("series_description"),
	}
}

// validate checks the form and converts it to store input.
func (f postForm) validate() (store.PostInput, string) {
	in := store.PostInput{
		Title: f.Title, Slug: f.Slug, Description: f.Description, CoverURL: f.CoverURL,
		BodyMD:      strings.ReplaceAll(f.Body, "\r\n", "\n"),
		SeriesTitle: f.SeriesTitle, SeriesDescription: f.SeriesDescription,
	}
	switch n := utf8.RuneCountInString(in.Title); {
	case n == 0:
		return in, "Title is required."
	case n > 200:
		return in, "Title is too long (200 characters max)."
	}
	if utf8.RuneCountInString(in.Description) > 300 {
		return in, "Description is too long (300 characters max)."
	}
	if len(in.BodyMD) > maxBodyBytes {
		return in, "Post body is too long (200 KB max). plurb stores text only."
	}
	if in.CoverURL != "" {
		u, err := url.Parse(in.CoverURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || len(in.CoverURL) > 1000 {
			return in, "Cover image must be an https:// link. Images are linked, never uploaded."
		}
	}
	if in.Slug != "" && in.Slug != store.Slug(in.Slug, "post", 120) {
		return in, "Slug may only use a–z, 0–9 and single dashes."
	}
	if utf8.RuneCountInString(in.SeriesTitle) > 200 || utf8.RuneCountInString(in.SeriesDescription) > 300 {
		return in, "Series title or description is too long."
	}
	seen := map[string]bool{}
	for _, t := range strings.Fields(f.Tags) {
		t = strings.TrimPrefix(t, "#")
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if utf8.RuneCountInString(t) > 64 {
			return in, "Tags must be 64 characters or fewer."
		}
		seen[strings.ToLower(t)] = true
		in.Tags = append(in.Tags, t)
	}
	if len(in.Tags) > maxTags {
		return in, "At most 10 tags per post."
	}
	return in, ""
}

func (s *Server) editPageData(r *http.Request, d editPage) (editPage, error) {
	u := currentUser(r)
	var err error
	if d.AuthorTags, err = s.store.AuthorTags(r.Context(), u.ID); err != nil {
		return d, err
	}
	d.Series, err = s.store.SeriesByAuthor(r.Context(), u.ID)
	return d, err
}

func (s *Server) renderEdit(w http.ResponseWriter, r *http.Request, status int, d editPage) {
	d, err := s.editPageData(r, d)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	title := "New post"
	if d.PostID != 0 {
		title = "Edit post"
	}
	s.render(w, r, status, "edit", s.newView(r, title, d))
}

func (s *Server) newPostPage(w http.ResponseWriter, r *http.Request) {
	s.renderEdit(w, r, http.StatusOK, editPage{Status: store.StatusDraft})
}

func (s *Server) createPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	u := currentUser(r)
	if !s.allow(w, r, "write:user:"+strconv.FormatInt(u.ID, 10), writesPerUser, writesWindow) {
		return
	}
	form := readPostForm(r)
	in, problem := form.validate()
	if problem == "" {
		in.Slug, problem = s.resolveSlug(r, u.ID, 0, form.Slug, in.Title)
	}
	if problem != "" {
		s.renderEdit(w, r, http.StatusUnprocessableEntity, editPage{Form: form, Error: problem, Status: store.StatusDraft})
		return
	}
	id, err := s.store.CreatePost(r.Context(), u.ID, in)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if r.PostFormValue("action") == "publish" {
		if err := s.store.SetPublished(r.Context(), u.ID, id, true); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	http.Redirect(w, r, "/panorama/editor/posts/"+strconv.FormatInt(id, 10)+"?saved=1", http.StatusSeeOther)
}

// resolveSlug validates an explicit slug or derives a free one from the title.
func (s *Server) resolveSlug(r *http.Request, authorID, postID int64, explicit, title string) (string, string) {
	if explicit != "" {
		taken, err := s.store.SlugTaken(r.Context(), authorID, explicit, postID)
		if err != nil {
			return "", "Couldn't check the slug, please retry."
		}
		if taken {
			return "", "You already have a post with that slug."
		}
		return explicit, ""
	}
	slug, err := s.store.FreeSlug(r.Context(), authorID, store.Slug(title, "post", 100), postID)
	if err != nil {
		return "", "Couldn't generate a slug, please retry."
	}
	return slug, ""
}

// ownPost loads a post by the {id} path value, 404ing for anyone but its author.
func (s *Server) ownPost(w http.ResponseWriter, r *http.Request) (*store.Post, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil, false
	}
	p, err := s.store.PostByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && p.AuthorID != currentUser(r).ID) {
		s.notFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return p, true
}

func (s *Server) editPostPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ownPost(w, r)
	if !ok {
		return
	}
	seriesTitle := ""
	if p.SeriesID.Valid {
		se, err := s.store.SeriesByID(r.Context(), p.SeriesID.Int64)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		seriesTitle = se.Title
	}
	d := editPage{PostID: p.ID, Status: p.Status, Form: formFromPost(p, seriesTitle), Saved: r.URL.Query().Has("saved")}
	s.renderEdit(w, r, http.StatusOK, d)
}

func (s *Server) updatePost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	p, ok := s.ownPost(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if !s.allow(w, r, "write:user:"+strconv.FormatInt(u.ID, 10), writesPerUser, writesWindow) {
		return
	}
	form := readPostForm(r)
	in, problem := form.validate()
	if problem == "" {
		in.Slug, problem = s.resolveSlug(r, u.ID, p.ID, form.Slug, in.Title)
	}
	if problem != "" {
		s.renderEdit(w, r, http.StatusUnprocessableEntity, editPage{PostID: p.ID, Status: p.Status, Form: form, Error: problem})
		return
	}
	if err := s.store.UpdatePost(r.Context(), u.ID, p.ID, in); err != nil {
		s.serverError(w, r, err)
		return
	}
	if r.PostFormValue("action") == "publish" && !p.Published() {
		if err := s.store.SetPublished(r.Context(), u.ID, p.ID, true); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	http.Redirect(w, r, "/panorama/editor/posts/"+strconv.FormatInt(p.ID, 10)+"?saved=1", http.StatusSeeOther)
}

func (s *Server) togglePublish(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ownPost(w, r)
	if !ok {
		return
	}
	if err := s.store.SetPublished(r.Context(), p.AuthorID, p.ID, !p.Published()); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/panorama/editor", http.StatusSeeOther)
}

func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	p, ok := s.ownPost(w, r)
	if !ok {
		return
	}
	if err := s.store.DeletePost(r.Context(), p.AuthorID, p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/panorama/editor", http.StatusSeeOther)
}

// previewMarkdown renders the editor's textarea through the same safe
// renderer as published posts and returns an HTML fragment.
func (s *Server) previewMarkdown(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if !s.allow(w, r, "preview:user:"+strconv.FormatInt(currentUser(r).ID, 10), previewPerUser, previewWindow) {
		return
	}
	body := r.PostFormValue("body")
	if len(body) > maxBodyBytes {
		http.Error(w, "too long", http.StatusRequestEntityTooLarge)
		return
	}
	out, err := s.md.Render(body)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(out.HTML))
}

// ---- about / profile ----

type aboutForm struct {
	DisplayName string
	About       string
	Error       string
	Saved       bool
}

func (s *Server) aboutPage(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	s.render(w, r, http.StatusOK, "about_edit", s.newView(r, "Edit profile", aboutForm{
		DisplayName: u.DisplayName, About: u.AboutMD, Saved: r.URL.Query().Has("saved"),
	}))
}

func (s *Server) saveAbout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	u := currentUser(r)
	f := aboutForm{
		DisplayName: strings.TrimSpace(r.PostFormValue("display_name")),
		About:       strings.ReplaceAll(r.PostFormValue("about"), "\r\n", "\n"),
	}
	switch {
	case f.DisplayName == "" || utf8.RuneCountInString(f.DisplayName) > 64:
		f.Error = "Display name must be 1–64 characters."
	case len(f.About) > 50_000:
		f.Error = "About is too long (50 KB max)."
	}
	if f.Error != "" {
		s.render(w, r, http.StatusUnprocessableEntity, "about_edit", s.newView(r, "Edit profile", f))
		return
	}
	if err := s.store.UpdateProfile(r.Context(), u.ID, f.DisplayName, f.About); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/panorama/editor/about?saved=1", http.StatusSeeOther)
}

// ---- account ----

type accountPage struct {
	HasPassword     bool
	GitHubLinked    bool
	UsernameError   string
	PasswordError   string
	PasswordChanged bool
}

func (s *Server) accountData(r *http.Request) (accountPage, error) {
	u := currentUser(r)
	linked, err := s.store.HasIdentity(r.Context(), u.ID, "github")
	return accountPage{HasPassword: u.PasswordHash != "", GitHubLinked: linked,
		PasswordChanged: r.URL.Query().Has("password_changed")}, err
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, status int, mutate func(*accountPage)) {
	d, err := s.accountData(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if mutate != nil {
		mutate(&d)
	}
	s.render(w, r, status, "account", s.newView(r, "Account", d))
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, r, http.StatusOK, nil)
}

// confirmPassword checks the current password; OAuth-only accounts have none.
func confirmPassword(u *store.User, password string) bool {
	return u.PasswordHash == "" || auth.Verify(u.PasswordHash, password)
}

func (s *Server) changeUsername(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("username")))
	fail := func(msg string) {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, func(d *accountPage) { d.UsernameError = msg })
	}
	if !confirmPassword(u, r.PostFormValue("password")) {
		fail("Current password is incorrect.")
		return
	}
	if !store.ValidUsername(name) {
		fail("Username must be 3–32 characters (a–z, 0–9, _) and not a reserved word.")
		return
	}
	err := s.store.UpdateUsername(r.Context(), u.ID, name)
	if errors.Is(err, store.ErrConflict) {
		fail("That username is taken.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/panorama/editor/account", http.StatusSeeOther)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	fail := func(msg string) {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, func(d *accountPage) { d.PasswordError = msg })
	}
	if !confirmPassword(u, r.PostFormValue("old_password")) {
		fail("Current password is incorrect.")
		return
	}
	hash, err := auth.Hash(r.PostFormValue("new_password"))
	if err != nil {
		fail(err.Error())
		return
	}
	if err := s.store.UpdatePassword(r.Context(), u.ID, hash); err != nil {
		s.serverError(w, r, err)
		return
	}
	// Sign out every other device, then start a fresh session here.
	if err := s.store.DeleteUserSessions(r.Context(), u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/panorama/editor/account?password_changed=1", http.StatusSeeOther)
}
