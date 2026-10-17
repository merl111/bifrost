package domain

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestIDsAreOpaqueAndFilenameSafe(t *testing.T) {
	for _, kind := range []Kind{KindProject, KindRepo, KindEvent} {
		id, err := NewID(kind)
		if err != nil {
			t.Fatalf("NewID(%s): %v", kind, err)
		}
		if got, err := ParseID(id.String()); err != nil || got != id {
			t.Fatalf("ParseID(%q) = %q, %v", id, got, err)
		}
		if filepath.Base(id.String()) != id.String() || strings.ContainsAny(id.String(), `/\\`) {
			t.Fatalf("unsafe ID %q", id)
		}
	}
	for _, value := range []string{"", "prj_", "prj_zz", "prj_00000000000000000000000000000000/../x", "unknown_00000000000000000000000000000000"} {
		if _, err := ParseID(value); err == nil {
			t.Errorf("ParseID(%q) unexpectedly succeeded", value)
		}
	}
}
