package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/chenbenny31/plurb/internal/auth"
	"github.com/chenbenny31/plurb/internal/markdown"
	"github.com/chenbenny31/plurb/internal/store"
)

const (
	afterLogin  = "/panorama/editor"
	oauthCookie = "plurb_oauth_state"
)

type loginForm struct {
	Username string
	Next     string
	Error    string
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, afterLogin, http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", s.newView(r, "Log in", loginForm{Next: r.URL.Query().Get("next")}))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	username := strings.ToLower(strings.TrimSpace(r.PostFormValue("username")))
	password := r.PostFormValue("password")
	next := r.PostFormValue("next")

	u, err := s.store.UserByUsername(r.Context(), username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		auth.VerifyDummy(password)
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	if u == nil || !auth.Verify(u.PasswordHash, password) {
		s.render(w, r, http.StatusUnauthorized, "login", s.newView(r, "Log in",
			loginForm{Username: username, Next: next, Error: "Invalid username or password."}))
		return
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, safeNext(next, afterLogin), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.store.DeleteSession(r.Context(), c.Value); err != nil {
			s.log.Error("logout", "err", err)
		}
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---- sign-up ----

type signupForm struct {
	Username    string
	DisplayName string
	Error       string
}

func (s *Server) signupPage(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OpenSignup {
		s.notFound(w, r)
		return
	}
	s.render(w, r, http.StatusOK, "signup", s.newView(r, "Sign up", signupForm{}))
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.OpenSignup {
		s.notFound(w, r)
		return
	}
	f := signupForm{
		Username:    strings.ToLower(strings.TrimSpace(r.PostFormValue("username"))),
		DisplayName: strings.TrimSpace(r.PostFormValue("display_name")),
	}
	password := r.PostFormValue("password")
	fail := func(msg string) {
		f.Error = msg
		s.render(w, r, http.StatusUnprocessableEntity, "signup", s.newView(r, "Sign up", f))
	}
	if !store.ValidUsername(f.Username) {
		fail("Username must be 3–32 characters (lowercase letters, digits or _) and not a reserved word.")
		return
	}
	if f.DisplayName == "" {
		f.DisplayName = f.Username
	}
	if len([]rune(f.DisplayName)) > 64 {
		fail("Display name is too long.")
		return
	}
	if password != r.PostFormValue("password_confirm") {
		fail("Passwords don't match.")
		return
	}
	hash, err := auth.Hash(password)
	if err != nil {
		fail(err.Error())
		return
	}
	u, err := s.store.CreateUser(r.Context(), f.Username, f.DisplayName, hash, "member")
	if errors.Is(err, store.ErrConflict) {
		fail("That username is taken.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, afterLogin, http.StatusSeeOther)
}

// ---- GitHub OAuth ----

func (s *Server) githubStart(w http.ResponseWriter, r *http.Request) {
	if s.gh == nil {
		s.notFound(w, r)
		return
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		s.serverError(w, r, err)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(b)
	// Lax is required: the callback arrives as a top-level navigation from github.com.
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookie, Value: state, Path: "/auth/github", MaxAge: 600,
		HttpOnly: true, Secure: !s.cfg.Dev, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, s.gh.AuthCodeURL(state), http.StatusFound)
}

func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	if s.gh == nil {
		s.notFound(w, r)
		return
	}
	c, err := r.Cookie(oauthCookie)
	state := r.URL.Query().Get("state")
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		s.badRequest(w, r, "Login expired or was started elsewhere. Please try again.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Path: "/auth/github", MaxAge: -1})

	code := r.URL.Query().Get("code")
	if code == "" {
		s.badRequest(w, r, "GitHub didn't authorize the login.")
		return
	}
	gu, err := s.gh.Exchange(r.Context(), code)
	if err != nil {
		s.log.Warn("github exchange", "err", err)
		s.badRequest(w, r, "Couldn't complete GitHub login. Please try again.")
		return
	}

	u, err := s.store.UserByIdentity(r.Context(), "github", gu.Subject())
	switch {
	case err == nil:
		// Known identity: sign in.
	case !errors.Is(err, store.ErrNotFound):
		s.serverError(w, r, err)
		return
	case currentUser(r) != nil:
		// Signed in already: link GitHub to this account.
		u = currentUser(r)
		if err := s.store.LinkIdentity(r.Context(), u.ID, "github", gu.Subject(), gu.Login); err != nil {
			if errors.Is(err, store.ErrConflict) {
				s.badRequest(w, r, "This account already has a GitHub login linked.")
				return
			}
			s.serverError(w, r, err)
			return
		}
	case s.cfg.OpenSignup:
		u, err = s.createGitHubUser(r, gu)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
	default:
		s.badRequest(w, r, "Sign-up is closed. Ask an admin for an account, then link GitHub from your account page.")
		return
	}

	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, afterLogin, http.StatusSeeOther)
}

// createGitHubUser derives a free username from the GitHub login
// ("Some-Dev" -> "some_dev", then "some_dev2", ...).
func (s *Server) createGitHubUser(r *http.Request, gu *auth.GitHubUser) (*store.User, error) {
	base := strings.ReplaceAll(markdown.Slugify(gu.Login, false), "-", "_")
	if len(base) > 28 {
		base = base[:28]
	}
	for len(base) < 3 {
		base += "_"
	}
	display := gu.Name
	if display == "" {
		display = gu.Login
	}
	if r := []rune(display); len(r) > 64 {
		display = string(r[:64])
	}
	for i := 1; i <= 50; i++ {
		name := base
		if i > 1 || !store.ValidUsername(name) {
			name = base + strconv.Itoa(i)
		}
		u, err := s.store.CreateUserWithIdentity(r.Context(), name, display, "github", gu.Subject(), gu.Login)
		if !errors.Is(err, store.ErrConflict) {
			return u, err
		}
	}
	return nil, errors.New("no free username for github login " + gu.Login)
}
