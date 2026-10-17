package accounts

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOAuthLoopbackSignedJWKSAndValidation(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	var mu sync.Mutex
	var nonce, audience string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jwks":
			n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
			e := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})
			_, _ = fmt.Fprintf(w, `{"keys":[{"kid":"k1","kty":"RSA","n":%q,"e":%q}]}`, n, e)
		case "/token":
			_ = r.ParseForm()
			mu.Lock()
			n, a := nonce, audience
			mu.Unlock()
			id := testJWT(t, key, map[string]any{"iss": server.URL, "sub": "subject-1", "email": "user@example.com", "nonce": n, "aud": a, "exp": time.Now().Add(time.Hour).Unix()})
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "id_token": id, "token_type": "Bearer", "expires_in": 3600, "scope": "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	newOAuth := func(change func(*url.Values)) *OAuth {
		o, err := NewOAuth(OAuthConfig{HostID: "urn:uuid:test", Name: "main", AgentNameHint: "Bifrost", Issuer: server.URL, JWKSURL: server.URL + "/jwks", TokenURL: server.URL + "/token", RevokeURL: server.URL + "/revoke"})
		if err != nil {
			t.Fatal(err)
		}
		o.cfg.RequestURL = func(raw string) error {
			u, _ := url.Parse(raw)
			q := u.Query()
			if change != nil {
				change(&q)
			}
			mu.Lock()
			nonce = q.Get("nonce")
			audience = q.Get("test_aud")
			if audience == "" {
				audience = "issued-client"
			}
			mu.Unlock()
			cb := q.Get("redirect_uri")
			if err := assertCallbackPreflight(t, cb); err != nil {
				return err
			}
			probe, err := http.Get(cb)
			if err != nil {
				return err
			}
			probe.Body.Close()
			if probe.StatusCode != http.StatusNoContent {
				return fmt.Errorf("empty callback probe status = %d", probe.StatusCode)
			}
			cbURL, _ := url.Parse(cb)
			q2 := cbURL.Query()
			q2.Set("code", "code")
			q2.Set("state", q.Get("state"))
			q2.Set("client_id", "issued-client")
			cbURL.RawQuery = q2.Encode()
			req, err := http.NewRequest(http.MethodGet, cbURL.String(), nil)
			if err != nil {
				return err
			}
			req.Header.Set("Origin", "https://auth.openai.com")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("callback status = %d", resp.StatusCode)
			}
			if resp.Header.Get("Access-Control-Allow-Origin") != "https://auth.openai.com" || resp.Header.Get("Access-Control-Allow-Private-Network") != "true" {
				return fmt.Errorf("callback CORS = %v", resp.Header)
			}
			return nil
		}
		return o
	}
	a, c, err := newOAuth(nil).SignIn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if a.Subject != "subject-1" || c.AccessToken != "access-1" {
		t.Fatalf("unexpected login %#v %#v", a, c)
	}
	if _, _, err := newOAuth(func(q *url.Values) { q.Set("state", "wrong") }).SignIn(t.Context()); err == nil {
		t.Fatal("wrong state accepted")
	}
	if _, _, err := newOAuth(func(q *url.Values) { q.Set("nonce", "wrong") }).SignIn(t.Context()); err == nil {
		t.Fatal("wrong nonce accepted")
	}
	if _, _, err := newOAuth(func(q *url.Values) { q.Set("test_aud", "wrong") }).SignIn(t.Context()); err == nil {
		t.Fatal("wrong audience accepted")
	}
}

func TestOAuthRefreshRotationAndRevokeFailure(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("access-%d", n), "refresh_token": fmt.Sprintf("refresh-%d", n), "token_type": "Bearer", "expires_in": 3600, "scope": "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	o, _ := NewOAuth(OAuthConfig{HostID: "urn:uuid:test", Issuer: srv.URL, TokenURL: srv.URL + "/token", RevokeURL: srv.URL + "/revoke", JWKSURL: srv.URL + "/jwks"})
	d := t.TempDir()
	s1, _ := New(d)
	s2, _ := New(d)
	a := Account{ID: "chatgpt-main", Name: "main", Kind: KindChatGPT, Subject: "s", Issuer: srv.URL, ClientID: "client"}
	if err := s1.Save(a, Credentials{RefreshToken: "refresh-0", Scopes: requiredChatGPTScopes}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, s := range []*Store{s1, s2} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			if err := s.RefreshContext(t.Context(), a.ID, func(c Credentials) (Credentials, error) { return o.Refresh(t.Context(), a, c) }); err != nil {
				t.Errorf("refresh: %v", err)
			}
		}(s)
	}
	wg.Wait()
	c, err := s1.LoadCredentials(a.ID)
	if err != nil || !strings.HasPrefix(c.RefreshToken, "refresh-") {
		t.Fatalf("rotation %#v %v", c, err)
	}
	if err := o.Revoke(t.Context(), c); err == nil {
		t.Fatal("revoke failure accepted")
	}
}

func TestLogoutClearsCredentialsButRetainsRegistration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
	defer srv.Close()
	o, _ := NewOAuth(OAuthConfig{HostID: "urn:uuid:test", Issuer: srv.URL, TokenURL: srv.URL + "/token", RevokeURL: srv.URL + "/revoke", JWKSURL: srv.URL + "/jwks"})
	s, _ := New(t.TempDir())
	a := Account{ID: "chatgpt-main", Name: "main", Kind: KindChatGPT, Subject: "s", Issuer: srv.URL, ClientID: "issued-client"}
	if err := s.Save(a, Credentials{AccessToken: "access", RefreshToken: "refresh"}); err != nil {
		t.Fatal(err)
	}
	if err := o.Logout(t.Context(), s, a.ID); err == nil || !strings.Contains(err.Error(), "local credentials cleared") {
		t.Fatalf("expected actionable revoke error: %v", err)
	}
	got, err := s.Get(a.ID)
	if err != nil || got.ClientID != a.ClientID {
		t.Fatalf("registration removed: %#v %v", got, err)
	}
	creds, err := s.LoadCredentials(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessToken != "" || creds.RefreshToken != "" {
		t.Fatalf("credentials retained: %#v", creds)
	}
}

func assertCallbackPreflight(t *testing.T, redirect string) error {
	t.Helper()
	denied, err := http.NewRequest(http.MethodOptions, redirect, nil)
	if err != nil {
		return err
	}
	denied.Header.Set("Origin", "https://evil.example")
	denied.Header.Set("Access-Control-Request-Private-Network", "true")
	deniedResp, err := http.DefaultClient.Do(denied)
	if err != nil {
		return err
	}
	deniedResp.Body.Close()
	if deniedResp.Header.Get("Access-Control-Allow-Origin") != "" {
		return fmt.Errorf("untrusted origin was allowed: %q", deniedResp.Header.Get("Access-Control-Allow-Origin"))
	}
	req, err := http.NewRequest(http.MethodOptions, redirect, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Origin", "https://auth.openai.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("preflight status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "https://auth.openai.com" || resp.Header.Get("Access-Control-Allow-Private-Network") != "true" || !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "GET") {
		return fmt.Errorf("preflight headers = %v", resp.Header)
	}
	return nil
}

func testJWT(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	p, _ := json.Marshal(claims)
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	signing := enc(h) + "." + enc(p)
	sum := sha256.Sum256([]byte(signing))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	return signing + "." + enc(sig)
}
