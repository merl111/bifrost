package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientOperationsAndAuthHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"octocat"}`))
		case "/repos/o/r":
			_, _ = w.Write([]byte(`{"id":1,"full_name":"o/r","default_branch":"main"}`))
		case "/repos/o/r/pulls":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[{"number":2,"title":"x","draft":true}]`))
			} else {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["draft"] != true {
					t.Errorf("body=%v", body)
				}
				_, _ = w.Write([]byte(`{"number":3,"title":"new","draft":true}`))
			}
		case "/repos/o/r/commits/main/check-runs":
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"id":4,"name":"ci","status":"completed","conclusion":"success"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := c.AuthStatus(context.Background())
	if err != nil || !auth.Authenticated || auth.Login != "octocat" {
		t.Fatalf("auth=%+v err=%v", auth, err)
	}
	repo, err := c.InspectRepository(context.Background(), "o", "r")
	if err != nil || repo.FullName != "o/r" {
		t.Fatalf("repo=%+v err=%v", repo, err)
	}
	prs, err := c.ListPullRequests(context.Background(), "o", "r", "", 1, 10)
	if err != nil || len(prs) != 1 || !prs[0].Draft {
		t.Fatalf("prs=%+v err=%v", prs, err)
	}
	checks, err := c.ListChecks(context.Background(), "o", "r", "main", 1, 10)
	if err != nil || checks.Total != 1 {
		t.Fatalf("checks=%+v err=%v", checks, err)
	}
	pr, err := c.CreateDraftPR(context.Background(), DraftPRRequest{Owner: "o", Repo: "r", Title: "new", Head: "feature", Base: "main"})
	if err != nil || pr.Number != 3 {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
}

func TestClientBoundsAndErrors(t *testing.T) {
	if _, _, err := ParseSlug("o/r/x"); err == nil {
		t.Fatal("accepted invalid slug")
	}
	if _, _, err := ParseSlug("o/--"); err == nil {
		t.Fatal("accepted invalid repo")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "4")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c, _ := New(Config{BaseURL: srv.URL})
	_, err := c.InspectRepository(context.Background(), "o", "r")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 || apiErr.RetryAfter != "4" {
		t.Fatalf("err=%v", err)
	}
	if _, err := New(Config{BaseURL: "http://example.com"}); err == nil {
		t.Fatal("accepted non-loopback HTTP")
	}
}
