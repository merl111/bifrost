package agent

import (
	"strings"
	"unicode/utf8"
)

// MaxEventDetailBytes bounds tool and provider detail retained for a UI
// event. It keeps expandable records useful without allowing a large tool
// result or image-adjacent payload into the event stream.
const MaxEventDetailBytes = 16 << 10

// BoundedEventDetail returns valid UTF-8 detail capped at the UI event limit.
// The suffix is included in the limit so callers can safely serialize it.
func BoundedEventDetail(value string) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= MaxEventDetailBytes {
		return value
	}
	const suffix = "…"
	limit := MaxEventDetailBytes - len(suffix)
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit] + suffix
}
