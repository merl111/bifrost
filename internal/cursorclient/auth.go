package cursorclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultWebsite = "https://cursor.com"
	defaultAPI     = "https://api2.cursor.sh"
	// productionClientID is the desktop client's refresh client id from Cursor 3.23.23.
	productionClientID = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"
)

// Tokens are the credentials returned by browser login, API-key exchange, or refresh.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	AuthID       string
	TeamID       string
}

// Login is one pending browser challenge. The verifier stays with the client
// that created it; the browser URL does not carry a token.
type Login struct {
	URL      string
	UUID     string
	Verifier string
}

// Auth talks to Cursor's login and token endpoints. It does not import desktop
// credentials or accept token-bearing callback URLs.
type Auth struct {
	HTTP         *http.Client
	Website      string
	API          string
	ClientID     string
	PollInterval func(attempt int) time.Duration
	MaxAttempts  int
}

func (a Auth) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return http.DefaultClient
}

func (a Auth) website() string {
	if strings.TrimSpace(a.Website) == "" {
		return defaultWebsite
	}
	return strings.TrimRight(a.Website, "/")
}

func (a Auth) api() string {
	if strings.TrimSpace(a.API) == "" {
		return defaultAPI
	}
	return strings.TrimRight(a.API, "/")
}

func (a Auth) clientID() string {
	if strings.TrimSpace(a.ClientID) == "" {
		return productionClientID
	}
	return a.ClientID
}

// BeginLogin creates a PKCE-style challenge and the browser URL for it.
func (a Auth) BeginLogin() (Login, error) {
	var verifierBytes [32]byte
	if _, err := rand.Read(verifierBytes[:]); err != nil {
		return Login{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	id, err := newUUID()
	if err != nil {
		return Login{}, err
	}
	q := url.Values{}
	q.Set("challenge", challenge)
	q.Set("uuid", id)
	q.Set("mode", "login")
	q.Set("redirectTarget", "cli")
	return Login{URL: a.website() + "/loginDeepControl?" + q.Encode(), UUID: id, Verifier: verifier}, nil
}

// Poll waits until the browser login finishes. A 404 means it is not ready.
// Three consecutive failures other than 404 stop the wait.
func (a Auth) Poll(ctx context.Context, pending Login) (Tokens, error) {
	if pending.UUID == "" || pending.Verifier == "" {
		return Tokens{}, errors.New("cursor login is incomplete")
	}
	attempts := a.MaxAttempts
	if attempts <= 0 {
		attempts = 150
	}
	failures := 0
	for i := 0; i < attempts; i++ {
		if err := ctx.Err(); err != nil {
			return Tokens{}, err
		}
		if i > 0 {
			if err := sleep(ctx, a.delay(i)); err != nil {
				return Tokens{}, err
			}
		}
		tok, status, err := a.pollOnce(ctx, pending)
		if err != nil {
			return Tokens{}, err
		}
		switch status {
		case http.StatusNotFound:
			failures = 0
			continue
		case http.StatusOK:
			if tok.AccessToken == "" {
				return Tokens{}, errors.New("cursor login returned no access token")
			}
			return tok, nil
		case http.StatusForbidden:
			return Tokens{}, errors.New("cursor sign-in was denied by policy")
		default:
			failures++
			if failures >= 3 {
				return Tokens{}, fmt.Errorf("cursor login poll failed with status %d", status)
			}
		}
	}
	return Tokens{}, errors.New("cursor login timed out")
}

func (a Auth) delay(attempt int) time.Duration {
	if a.PollInterval != nil {
		return a.PollInterval(attempt)
	}
	d := time.Second
	for i := 1; i < attempt; i++ {
		d = time.Duration(float64(d) * 1.2)
		if d > 10*time.Second {
			return 10 * time.Second
		}
	}
	return d
}

func (a Auth) pollOnce(ctx context.Context, pending Login) (Tokens, int, error) {
	q := url.Values{}
	q.Set("uuid", pending.UUID)
	q.Set("verifier", pending.Verifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.api()+"/auth/poll?"+q.Encode(), nil)
	if err != nil {
		return Tokens{}, 0, err
	}
	res, err := a.client().Do(req)
	if err != nil {
		return Tokens{}, 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Tokens{}, res.StatusCode, err
	}
	if res.StatusCode != http.StatusOK {
		return Tokens{}, res.StatusCode, nil
	}
	tok, err := decodeTokens(body)
	return tok, res.StatusCode, err
}

// ExchangeAPIKey trades a Cursor user API key for access and refresh tokens.
func (a Auth) ExchangeAPIKey(ctx context.Context, apiKey string) (Tokens, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return Tokens{}, errors.New("cursor API key is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.api()+"/auth/exchange_user_api_key", strings.NewReader("{}"))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	return a.tokenPost(req)
}

// Refresh exchanges a refresh token. shouldLogout in the response is a forced sign-out.
func (a Auth) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return Tokens{}, errors.New("cursor refresh token is empty")
	}
	payload, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     a.clientID(),
		"refresh_token": refreshToken,
	})
	if err != nil {
		return Tokens{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.api()+"/oauth/token", bytes.NewReader(payload))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	tok, err := a.tokenPost(req)
	if err != nil {
		return Tokens{}, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

func (a Auth) tokenPost(req *http.Request) (Tokens, error) {
	res, err := a.client().Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Tokens{}, err
	}
	if res.StatusCode == http.StatusForbidden {
		return Tokens{}, errors.New("cursor rejected the credential")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Tokens{}, fmt.Errorf("cursor token request returned status %d", res.StatusCode)
	}
	var meta struct {
		ShouldLogout bool `json:"shouldLogout"`
	}
	_ = json.Unmarshal(body, &meta)
	if meta.ShouldLogout {
		return Tokens{}, errors.New("cursor signed this session out")
	}
	return decodeTokens(body)
}

func decodeTokens(body []byte) (Tokens, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return Tokens{}, errors.New("cursor token response was not JSON")
	}
	tok := Tokens{
		AccessToken:  firstString(raw, "accessToken", "access_token"),
		RefreshToken: firstString(raw, "refreshToken", "refresh_token"),
		AuthID:       firstString(raw, "authId", "auth_id"),
		TeamID:       firstString(raw, "selectedTeamId", "teamId", "team_id"),
	}
	if tok.AccessToken == "" {
		return Tokens{}, errors.New("cursor token response has no access token")
	}
	return tok, nil
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := raw[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// TokenExpiring reports whether a JWT access token expires within five minutes.
// The claim is an expiry hint; the signature is not verified.
func TokenExpiring(access string, now time.Time) bool {
	parts := strings.Split(access, ".")
	if len(parts) < 2 {
		return true
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		if payload, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return true
		}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return true
	}
	return !now.Add(5 * time.Minute).Before(time.Unix(claims.Exp, 0))
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		d = time.Millisecond
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
