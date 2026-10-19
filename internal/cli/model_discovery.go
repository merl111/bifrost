package cli

import (
	"bifrost/internal/accounts"
	"bifrost/internal/ui"
	"fmt"
	"strings"
)

// A saved profile's Account is authoritative. Profile IDs and account names are
// different namespaces; prefixing a profile ID again can select no account.
func discoveryAccount(store *accounts.Store, kind, profile, account string) (accounts.Account, error) {
	k := accounts.Kind(kind)
	if account != "" {
		return store.GetByName(k, account)
	}
	if a, err := store.Get(profile); err == nil && a.Kind == k {
		return a, nil
	}
	return store.GetByName(k, profile)
}

func (c *workspaceChat) catalogAPIKey(req ui.AccountAddRequest, kind, name string) (string, error) {
	if key := strings.TrimSpace(req.RawAPIKey); key != "" {
		return key, nil
	}
	if kind == "openrouter" && req.Account != "" {
		store, err := accountStore(c.opts)
		if err != nil {
			return "", err
		}
		a, err := discoveryAccount(store, kind, name, req.Account)
		if err != nil {
			return "", err
		}
		key := openRouterAccountKey(store, a)
		if key == "" {
			return "", fmt.Errorf("%w: reconnect OpenRouter account %q", errMissingAccountCredential, a.Name)
		}
		return key, nil
	}
	return envFirst(strings.TrimSpace(req.APIKeyEnv), "BIFROST_API_KEY", defaultAPIKeyEnv(kind), compatibleKeyEnv(kind)), nil
}
