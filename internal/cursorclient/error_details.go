package cursorclient

import (
	"encoding/binary"
	"strconv"
	"strings"
)

// cursorErrorNames are aiserver.v1.ErrorDetails.Error values. The Connect
// wrapper often says only "Error"; the enum and title carry the reason.
var cursorErrorNames = map[int]string{
	1:  "BAD_API_KEY",
	2:  "NOT_LOGGED_IN",
	3:  "INVALID_AUTH_ID",
	4:  "NOT_HIGH_ENOUGH_PERMISSIONS",
	5:  "BAD_MODEL_NAME",
	6:  "USER_NOT_FOUND",
	7:  "FREE_USER_RATE_LIMIT_EXCEEDED",
	8:  "PRO_USER_RATE_LIMIT_EXCEEDED",
	9:  "FREE_USER_USAGE_LIMIT",
	10: "PRO_USER_USAGE_LIMIT",
	11: "AUTH_TOKEN_NOT_FOUND",
	12: "AUTH_TOKEN_EXPIRED",
	14: "OPENAI_RATE_LIMIT_EXCEEDED",
	18: "AGENT_REQUIRES_LOGIN",
	20: "MAX_TOKENS",
	21: "USER_ABORTED_REQUEST",
	22: "GENERIC_RATE_LIMIT_EXCEEDED",
	23: "PRO_USER_ONLY",
	29: "CUSTOM_MESSAGE",
	30: "OUTDATED_CLIENT",
	34: "API_KEY_RATE_LIMIT",
	36: "BAD_REQUEST",
	41: "RESOURCE_EXHAUSTED",
	42: "BAD_USER_API_KEY",
	43: "CONVERSATION_TOO_LONG",
	44: "USAGE_PRICING_REQUIRED",
	45: "USAGE_PRICING_REQUIRED_CHANGEABLE",
	50: "RATE_LIMITED",
	51: "RATE_LIMITED_CHANGEABLE",
	58: "MODEL_BLOCKED",
	60: "MAX_MODE_REQUIRED",
	61: "MODEL_NO_LONGER_SUPPORTED",
	63: "SLOW_POOL",
	64: "UNSUPPORTED_REGION",
	65: "ACCOUNT_CLOSED",
}

func cursorErrorName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "UNSPECIFIED" || raw == "0" {
		return ""
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return cursorErrorNameByCode(n)
	}
	return raw
}

func cursorErrorNameByCode(code int) string {
	if name := cursorErrorNames[code]; name != "" {
		return name
	}
	if code <= 0 {
		return ""
	}
	return "ERROR_" + strconv.Itoa(code)
}

func decodeErrorDetails(b []byte) (code int, title, detail string) {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return code, title, detail
		}
		b = b[n:]
		field, wire := int(key>>3), int(key&7)
		switch wire {
		case 0:
			v, m := binary.Uvarint(b)
			if m <= 0 {
				return code, title, detail
			}
			b = b[m:]
			if field == 1 {
				code = int(v)
			}
		case 2:
			ln, m := binary.Uvarint(b)
			if m <= 0 || int(ln) > len(b[m:]) {
				return code, title, detail
			}
			b = b[m:]
			body := b[:ln]
			b = b[ln:]
			if field == 2 {
				title, detail = decodeCustomErrorDetails(body)
			}
		default:
			return code, title, detail
		}
	}
	return code, title, detail
}

func decodeCustomErrorDetails(b []byte) (title, detail string) {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return title, detail
		}
		b = b[n:]
		field, wire := int(key>>3), int(key&7)
		if wire != 2 {
			return title, detail
		}
		ln, m := binary.Uvarint(b)
		if m <= 0 || int(ln) > len(b[m:]) {
			return title, detail
		}
		b = b[m:]
		body := string(b[:ln])
		b = b[ln:]
		switch field {
		case 1:
			title = body
		case 2:
			detail = body
		}
	}
	return title, detail
}
