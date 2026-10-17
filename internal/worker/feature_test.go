package worker

import "testing"

func TestBrowserURLAndRemoteImageBounds(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://user:pass@example.test/", "https://"} {
		if err := validateBrowserURL(raw); err == nil {
			t.Fatalf("accepted unsafe browser URL %q", raw)
		}
	}
	if err := validateBrowserURL("https://example.test/path"); err != nil {
		t.Fatal(err)
	}
	if _, err := boundImage(make([]byte, MaxRemoteImageBytes+1)); err == nil {
		t.Fatal("oversized remote image accepted")
	}
	if _, err := boundImage([]byte("png")); err != nil {
		t.Fatal(err)
	}
}
