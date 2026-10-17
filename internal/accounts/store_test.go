package accounts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreSecretSeparationAndRotation(t *testing.T) {
	d := t.TempDir()
	s, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	a := Account{ID: "chatgpt-main", Name: "main", Kind: KindChatGPT, Subject: "sub-1", Issuer: "https://auth.openai.com", ClientID: "oaiapp_1"}
	if err := s.Save(a, Credentials{AccessToken: "access-secret", RefreshToken: "refresh-secret"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %#v", err, list)
	}
	b, _ := json.Marshal(list[0])
	if string(b) == "" || containsString(string(b), "secret") {
		t.Fatalf("secret escaped into summary: %s", b)
	}
	if mode := fileMode(t, filepath.Join(d, a.ID+".secret.json")); mode.Perm() != 0600 {
		t.Fatalf("secret mode %o", mode.Perm())
	}
	if got, err := s.Get(KindChatGPT, "main"); err != nil || got.ID != a.ID {
		t.Fatalf("named get: %#v %v", got, err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_ = s.Refresh(a.ID, func(c Credentials) (Credentials, error) { c.RefreshToken = c.RefreshToken + "-next"; return c, nil })
		}()
	}
	wg.Wait()
	c, err := s.LoadCredentials(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessToken != "access-secret" {
		t.Fatal("access token changed")
	}
	if err := s.Save(Account{ID: a.ID, Name: a.Name, Kind: a.Kind, Subject: "different", Issuer: a.Issuer, ClientID: a.ClientID}, c); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("identity replacement err=%v", err)
	}
}

func fileMode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode()
}
func containsString(s, part string) bool {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
