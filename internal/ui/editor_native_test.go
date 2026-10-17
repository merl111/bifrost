package ui

import "testing"

func TestNativeEditorSearchIncludesCurrentUnicodeMatch(t *testing.T) {
	b := NewEditorBuffer(EditorDocument{Content: "🌉 sole match"})
	b.SetCursor(len("🌉 "))
	if !b.Find("sole") || b.Cursor() != len("🌉 ") {
		t.Fatal("sole match at current cursor was reported absent")
	}
	if b.Find("absent") {
		t.Fatal("absent query matched")
	}
	if b.Find("") {
		t.Fatal("empty query matched")
	}
}
