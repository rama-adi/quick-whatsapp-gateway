package service

import "strings"

func isLID(jid string) bool {
	return strings.HasSuffix(jid, "@lid")
}

func phoneFromJID(jid string) string {
	const suffix = "@s.whatsapp.net"
	if !strings.HasSuffix(jid, suffix) {
		return ""
	}
	// Device-qualified JIDs carry a :device suffix; it is not part of the phone.
	phone, _, _ := strings.Cut(strings.TrimSuffix(jid, suffix), ":")
	return phone
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func int64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}
