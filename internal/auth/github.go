package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub implements the OAuth2 authorization-code flow against GitHub.
// The endpoint URLs are fields so tests can point them at a fake server.
type GitHub struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string

	AuthURL  string
	TokenURL string
	UserURL  string
	HTTP     *http.Client
}

func NewGitHub(clientID, clientSecret, redirectURL string) *GitHub {
	return &GitHub{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		AuthURL:      "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		UserURL:      "https://api.github.com/user",
		HTTP:         &http.Client{Timeout: 10 * time.Second},
	}
}

// GitHubUser is the subset of GET /user we use. ID is stable; Login can change.
type GitHubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

func (u GitHubUser) Subject() string { return strconv.FormatInt(u.ID, 10) }

// AuthCodeURL is where to send the browser to start the flow.
func (g *GitHub) AuthCodeURL(state string) string {
	q := url.Values{
		"client_id":    {g.ClientID},
		"redirect_uri": {g.RedirectURL},
		"scope":        {"read:user"},
		"state":        {state},
		"allow_signup": {"true"},
	}
	return g.AuthURL + "?" + q.Encode()
}

// Exchange trades the callback's code for a token, then fetches the user.
func (g *GitHub) Exchange(ctx context.Context, code string) (*GitHubUser, error) {
	form := url.Values{
		"client_id":     {g.ClientID},
		"client_secret": {g.ClientSecret},
		"code":          {code},
		"redirect_uri":  {g.RedirectURL},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", g.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := g.doJSON(req, &tok); err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("token exchange: %s %s", tok.Error, tok.ErrorDesc)
	}

	req, err = http.NewRequestWithContext(ctx, "GET", g.UserURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	var u GitHubUser
	if err := g.doJSON(req, &u); err != nil {
		return nil, fmt.Errorf("fetch user: %w", err)
	}
	if u.ID == 0 || u.Login == "" {
		return nil, errors.New("fetch user: empty profile")
	}
	return &u, nil
}

func (g *GitHub) doJSON(req *http.Request, v any) error {
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.Unmarshal(body, v)
}
