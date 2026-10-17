package accounts

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultAuthURL  = "https://auth.openai.com/api/accounts/authorize"
	defaultTokenURL = "https://auth.openai.com/api/accounts/oauth/token"
	defaultResource = "https://api.openai.com/v1"
)

var requiredChatGPTScopes = []string{"openid", "profile", "email", "offline_access", "resource.invoke", "chatgpt.tokens.use.direct"}

type OAuthConfig struct {
	ClientID               string
	AgentNameHint          string
	Name                   string
	HostID                 string
	AuthURL                string
	TokenURL               string
	JWKSURL                string
	Issuer                 string
	RevokeURL              string
	OpenIDConfigurationURL string
	Resource               string
	Scopes                 []string
	CallbackPath           string
	IDTokenHint            string
	LoginHint              string
	HTTPClient             *http.Client
	RequestURL             func(string) error
}

type OAuth struct {
	cfg    OAuthConfig
	client *http.Client
	mu     sync.Mutex
}

func NewOAuth(cfg OAuthConfig) (*OAuth, error) {
	if cfg.AuthURL == "" {
		cfg.AuthURL = defaultAuthURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = defaultTokenURL
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "https://auth.openai.com"
	}
	if cfg.OpenIDConfigurationURL == "" {
		cfg.OpenIDConfigurationURL = "https://auth.openai.com/.well-known/openid-configuration"
	}
	if cfg.Resource == "" {
		cfg.Resource = defaultResource
	}
	if cfg.CallbackPath == "" {
		cfg.CallbackPath = "/auth/callback"
	}
	if !strings.HasPrefix(cfg.CallbackPath, "/") || strings.ContainsAny(cfg.CallbackPath, "\r\n") {
		return nil, errors.New("accounts: invalid OAuth callback path")
	}
	if cfg.HostID == "" {
		return nil, errors.New("accounts: OAuth host ID is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	c := *cfg.HTTPClient
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &OAuth{cfg: cfg, client: &c}, nil
}

type oauthCallback struct{ code, state, clientID, scope, err, errDescription string }
type OAuthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// SignIn performs the public-client loopback PKCE flow. requestURL receives
// the URL to present to a browser; no browser is opened by this package.
func (o *OAuth) SignIn(ctx context.Context, requestURL ...func(string) error) (Account, Credentials, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.discover(ctx, false); err != nil {
		return Account{}, Credentials{}, err
	}
	if err := ctx.Err(); err != nil {
		return Account{}, Credentials{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Account{}, Credentials{}, fmt.Errorf("accounts: OAuth callback listener: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	redirect := fmt.Sprintf("http://127.0.0.1:%d%s", port, o.cfg.CallbackPath)
	state, err := randomURLValue(32)
	if err != nil {
		return Account{}, Credentials{}, err
	}
	nonce, err := randomURLValue(32)
	if err != nil {
		return Account{}, Credentials{}, err
	}
	verifier, err := randomURLValue(48)
	if err != nil {
		return Account{}, Credentials{}, err
	}
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])
	clientID := o.cfg.ClientID
	if clientID == "" {
		clientID = "dynamic_agent_client"
	}
	q := url.Values{"client_id": {clientID}, "ext_agent_host_id": {o.cfg.HostID}, "response_type": {"code"}, "redirect_uri": {redirect}, "scope": {strings.Join(o.scopes(), " ")}, "resource": {o.cfg.Resource}, "state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "code_challenge": {challenge}}
	if clientID == "dynamic_agent_client" {
		if o.cfg.AgentNameHint == "" {
			return Account{}, Credentials{}, errors.New("accounts: agent name hint is required for dynamic registration")
		}
		q.Set("agent_name_hint", o.cfg.AgentNameHint)
	} else {
		if o.cfg.IDTokenHint != "" {
			q.Set("id_token_hint", o.cfg.IDTokenHint)
		}
		if o.cfg.LoginHint != "" {
			q.Set("login_hint", o.cfg.LoginHint)
		}
	}
	endpoint, err := url.Parse(o.cfg.AuthURL)
	if err != nil {
		return Account{}, Credentials{}, errors.New("accounts: invalid OAuth authorization URL")
	}
	endpoint.RawQuery = q.Encode()
	cb := o.cfg.RequestURL
	if len(requestURL) > 0 && requestURL[0] != nil {
		cb = requestURL[0]
	}
	resultCh := make(chan oauthCallback, 1)
	server := http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != o.cfg.CallbackPath {
			http.NotFound(w, r)
			return
		}
		// The consent page completes sign-in with a cross-origin fetch from
		// https://auth.openai.com to this loopback URL. Chrome blocks that
		// public-to-local request unless the preflight opts into private
		// network access. A rejected preflight never sends the code.
		setCallbackCORS(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		values := r.URL.Query()
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			values = r.Form
		}
		result := oauthCallback{state: values.Get("state"), code: values.Get("code"), clientID: values.Get("client_id"), scope: values.Get("scope"), err: values.Get("error"), errDescription: values.Get("error_description")}
		if result.code == "" && result.err == "" {
			// Connection probes have neither a code nor an OAuth error.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case resultCh <- result:
		default:
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "Authentication complete. You may close this window.\n")
	})}
	go server.Serve(listener)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if cb != nil {
		if err := cb(endpoint.String()); err != nil {
			return Account{}, Credentials{}, err
		}
	}
	var result oauthCallback
	select {
	case result = <-resultCh:
	case <-ctx.Done():
		return Account{}, Credentials{}, ctx.Err()
	}
	if result.state != state {
		return Account{}, Credentials{}, errors.New("accounts: OAuth state mismatch")
	}
	if result.err != "" {
		return Account{}, Credentials{}, fmt.Errorf("accounts: OAuth authorization failed (%s)", result.err)
	}
	if result.code == "" {
		return Account{}, Credentials{}, errors.New("accounts: OAuth callback did not contain a code")
	}
	if clientID != "dynamic_agent_client" && result.clientID != "" && result.clientID != clientID {
		return Account{}, Credentials{}, errors.New("accounts: OAuth client ID mismatch")
	}
	if clientID == "dynamic_agent_client" {
		clientID = result.clientID
		if clientID == "" {
			return Account{}, Credentials{}, errors.New("accounts: dynamic registration did not issue a client ID")
		}
	}
	tok, err := o.exchange(ctx, clientID, result.code, verifier, redirect)
	if err != nil {
		return Account{}, Credentials{}, err
	}
	granted := parseScopes(tok.Scope)
	if len(granted) == 0 {
		granted = parseScopes(result.scope)
	}
	for _, required := range requiredChatGPTScopes {
		if !hasScope(granted, required) {
			return Account{}, Credentials{}, errors.New("accounts: OAuth grant lacks required scope")
		}
	}
	claims, err := o.verifyIDToken(ctx, tok.IDToken, clientID, nonce)
	if err != nil {
		return Account{}, Credentials{}, err
	}
	if claims.Subject == "" {
		return Account{}, Credentials{}, errors.New("accounts: ID token subject is missing")
	}
	name := o.cfg.Name
	if name == "" {
		name = accountName(claims.Email, claims.Subject)
	}
	if !validName.MatchString(name) {
		return Account{}, Credentials{}, errors.New("accounts: invalid account name")
	}
	id, err := StableID(KindChatGPT, name)
	if err != nil {
		return Account{}, Credentials{}, err
	}
	now := time.Now().UTC()
	expiry := now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	if tok.ExpiresIn <= 0 {
		expiry = time.Unix(claims.ExpiresAt, 0).UTC()
	}
	return Account{ID: id, Name: name, Kind: KindChatGPT, Email: claims.Email, Subject: claims.Subject, Issuer: claims.Issuer, ClientID: clientID, HostID: o.cfg.HostID, Scopes: granted, ExpiresAt: expiry, CreatedAt: now, UpdatedAt: now}, Credentials{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, IDToken: tok.IDToken, TokenType: tok.TokenType, Scopes: granted, ExpiresAt: expiry, SavedAt: now}, nil
}

func (o *OAuth) exchange(ctx context.Context, clientID, code, verifier, redirect string) (OAuthToken, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {o.cfg.Resource}}
	return o.tokenRequest(ctx, form)
}

func (o *OAuth) Refresh(ctx context.Context, a Account, c Credentials) (Credentials, error) {
	if err := o.discover(ctx, false); err != nil {
		return Credentials{}, err
	}
	if a.Kind != KindChatGPT || a.ClientID == "" || c.RefreshToken == "" {
		return Credentials{}, errors.New("accounts: invalid ChatGPT refresh credentials")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {a.ClientID}, "refresh_token": {c.RefreshToken}, "resource": {o.cfg.Resource}}
	tok, err := o.tokenRequest(ctx, form)
	if err != nil {
		return Credentials{}, err
	}
	granted := parseScopes(tok.Scope)
	if len(granted) == 0 {
		granted = c.Scopes
	}
	for _, required := range requiredChatGPTScopes {
		if !hasScope(granted, required) {
			return Credentials{}, errors.New("accounts: refreshed grant lacks required scope")
		}
	}
	if tok.IDToken != "" {
		claims, err := o.verifyIDToken(ctx, tok.IDToken, a.ClientID, "")
		if err != nil {
			return Credentials{}, err
		}
		if claims.Subject != a.Subject || claims.Issuer != a.Issuer {
			return Credentials{}, ErrIdentityMismatch
		}
	}
	out := c
	out.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		out.RefreshToken = tok.RefreshToken
	}
	if tok.IDToken != "" {
		out.IDToken = tok.IDToken
	}
	out.TokenType = tok.TokenType
	out.Scopes = granted
	out.ExpiresAt = time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second)
	out.SavedAt = time.Now().UTC()
	return out, nil
}

func (o *OAuth) tokenRequest(ctx context.Context, form url.Values) (OAuthToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthToken{}, errors.New("accounts: create OAuth token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return OAuthToken{}, errors.New("accounts: OAuth token request failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return OAuthToken{}, errors.New("accounts: read OAuth token response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return OAuthToken{}, fmt.Errorf("accounts: OAuth token endpoint returned HTTP %d", resp.StatusCode)
	}
	var tok OAuthToken
	if err := json.Unmarshal(b, &tok); err != nil || tok.AccessToken == "" {
		return OAuthToken{}, errors.New("accounts: invalid OAuth token response")
	}
	return tok, nil
}

func (o *OAuth) Revoke(ctx context.Context, c Credentials) error {
	if err := o.discover(ctx, true); err != nil {
		return err
	}
	if o.cfg.RevokeURL == "" {
		return errors.New("accounts: OAuth revocation endpoint unavailable")
	}
	if c.RefreshToken == "" && c.AccessToken == "" {
		return nil
	}
	form := url.Values{"token": {c.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {o.cfg.ClientID}}
	if c.RefreshToken == "" {
		form.Set("token", c.AccessToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.RevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return errors.New("accounts: OAuth revoke request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("accounts: OAuth revoke endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (o *OAuth) discover(ctx context.Context, needRevoke bool) error {
	if needRevoke && o.cfg.RevokeURL != "" {
		return nil
	}
	if !needRevoke && o.cfg.JWKSURL != "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.cfg.OpenIDConfigurationURL, nil)
	if err != nil {
		return errors.New("accounts: create OpenID discovery request")
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return errors.New("accounts: OpenID discovery request failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("accounts: OpenID discovery request failed")
	}
	var metadata struct {
		JWKSURI            string `json:"jwks_uri"`
		RevocationEndpoint string `json:"revocation_endpoint"`
	}
	if json.Unmarshal(b, &metadata) != nil {
		return errors.New("accounts: invalid OpenID discovery response")
	}
	if o.cfg.JWKSURL == "" {
		o.cfg.JWKSURL = metadata.JWKSURI
	}
	if o.cfg.RevokeURL == "" {
		o.cfg.RevokeURL = metadata.RevocationEndpoint
	}
	if (!needRevoke && o.cfg.JWKSURL == "") || (needRevoke && o.cfg.RevokeURL == "") {
		return errors.New("accounts: OpenID discovery response omitted required endpoint")
	}
	return nil
}

func (o *OAuth) Logout(ctx context.Context, s *Store, id string) error {
	c, err := s.LoadCredentials(id)
	if err != nil {
		return err
	}
	revokeErr := o.Revoke(ctx, c)
	clearErr := s.ClearCredentials(id)
	if clearErr != nil {
		return fmt.Errorf("accounts: logout could not clear local credentials: %w", clearErr)
	}
	if revokeErr != nil {
		return fmt.Errorf("accounts: local credentials cleared; remote revocation failed: %w", revokeErr)
	}
	return nil
}

type idClaims struct {
	Issuer    string          `json:"iss"`
	Subject   string          `json:"sub"`
	Email     string          `json:"email"`
	Nonce     string          `json:"nonce"`
	Audience  json.RawMessage `json:"aud"`
	ExpiresAt int64           `json:"exp"`
}

func (o *OAuth) verifyIDToken(ctx context.Context, raw, clientID, nonce string) (idClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return idClaims{}, errors.New("accounts: invalid ID token")
	}
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	p, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return idClaims{}, errors.New("accounts: invalid ID token")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(h, &head) != nil || head.Alg != "RS256" || head.Kid == "" {
		return idClaims{}, errors.New("accounts: unsupported ID token signing algorithm")
	}
	var cl idClaims
	if json.Unmarshal(p, &cl) != nil {
		return idClaims{}, errors.New("accounts: invalid ID token claims")
	}
	if cl.Issuer != o.cfg.Issuer || cl.Subject == "" || (nonce != "" && cl.Nonce != nonce) || cl.ExpiresAt <= time.Now().Unix() {
		return idClaims{}, errors.New("accounts: invalid ID token claims")
	}
	if !audienceMatches(cl.Audience, clientID) {
		return idClaims{}, errors.New("accounts: ID token audience mismatch")
	}
	key, err := o.jwk(ctx, head.Kid)
	if err != nil {
		return idClaims{}, err
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], sig); err != nil {
		return idClaims{}, errors.New("accounts: ID token signature verification failed")
	}
	return cl, nil
}

func (o *OAuth) jwk(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.cfg.JWKSURL, nil)
	if err != nil {
		return nil, errors.New("accounts: create JWKS request")
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, errors.New("accounts: JWKS request failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("accounts: JWKS request failed")
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if json.Unmarshal(b, &set) != nil {
		return nil, errors.New("accounts: invalid JWKS")
	}
	for _, k := range set.Keys {
		if k.Kid != kid || k.Kty != "RSA" {
			continue
		}
		nb, e1 := base64.RawURLEncoding.DecodeString(k.N)
		eb, e2 := base64.RawURLEncoding.DecodeString(k.E)
		if e1 != nil || e2 != nil {
			return nil, errors.New("accounts: invalid JWKS key")
		}
		n := new(big.Int).SetBytes(nb)
		var ebig big.Int
		ebig.SetBytes(eb)
		return &rsa.PublicKey{N: n, E: int(ebig.Int64())}, nil
	}
	return nil, errors.New("accounts: JWKS key not found")
}

func audienceMatches(raw json.RawMessage, want string) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == want
	}
	var a []string
	return json.Unmarshal(raw, &a) == nil && len(a) > 0 && contains(a, want)
}
func contains(a []string, v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
func setCallbackCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	switch origin {
	case "https://auth.openai.com", "https://chatgpt.com", "https://platform.openai.com":
	default:
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	requested := r.Header.Get("Access-Control-Request-Headers")
	if requested == "" {
		requested = "Content-Type"
	}
	w.Header().Set("Access-Control-Allow-Headers", requested)
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	w.Header().Set("Access-Control-Max-Age", "600")
}

func randomURLValue(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (o *OAuth) scopes() []string {
	if len(o.cfg.Scopes) > 0 {
		return append([]string(nil), o.cfg.Scopes...)
	}
	return append([]string(nil), requiredChatGPTScopes...)
}
func parseScopes(raw string) []string { parts := strings.Fields(raw); sortStrings(parts); return parts }
func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
func hasScope(scopes []string, want string) bool { return contains(scopes, want) }
func accountName(email, sub string) string {
	base := email
	if i := strings.IndexByte(base, '@'); i > 0 {
		base = base[:i]
	}
	if !validName.MatchString(base) {
		base = "account"
	}
	if len(base) > 48 {
		base = base[:48]
	}
	if base == "account" {
		sum := sha256.Sum256([]byte(sub))
		base += "-" + hexString(sum[:6])
	}
	return base
}
func hexString(b []byte) string { return fmt.Sprintf("%x", b) }
