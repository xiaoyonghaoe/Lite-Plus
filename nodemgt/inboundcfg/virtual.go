package inboundcfg

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	// VirtualInboundSep separates a physical inbound ID and its virtual tag ("inbID#tag").
	VirtualInboundSep = "#"
	// VirtualUserSep separates a base user ID and its virtual tag in core auth_user ("userID@tag").
	VirtualUserSep = "@"
)

// NormalizeVirtualTag validates and normalizes a single virtual inbound tag.
// Only ASCII letters, digits, '-' and '_' (1..24 chars) are allowed.
func NormalizeVirtualTag(raw string) string {
	tag := strings.TrimSpace(raw)
	if len(tag) == 0 || len(tag) > 24 {
		return ""
	}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return ""
	}
	return tag
}

// NormalizeVirtualTags deduplicates and validates a list of virtual tags.
func NormalizeVirtualTags(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		tag := NormalizeVirtualTag(item)
		if tag == "" {
			continue
		}
		lower := strings.ToLower(tag)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SplitVirtualInboundID splits "inbID#tag" into ("inbID", "tag").
// If no virtual tag is present, tag is "".
func SplitVirtualInboundID(id string) (baseID, tag string) {
	id = strings.TrimSpace(id)
	base, rest, ok := strings.Cut(id, VirtualInboundSep)
	if !ok {
		return id, ""
	}
	return strings.TrimSpace(base), NormalizeVirtualTag(rest)
}

// FormatVirtualInboundID builds "baseID#tag" when tag is valid, or returns baseID.
func FormatVirtualInboundID(baseID, tag string) string {
	baseID = strings.TrimSpace(baseID)
	tag = NormalizeVirtualTag(tag)
	if baseID == "" || tag == "" {
		return baseID
	}
	return baseID + VirtualInboundSep + tag
}

// VirtualUserKey returns "userID@tag" when tag is non-empty, else userID.
func VirtualUserKey(userID, tag string) string {
	userID = strings.TrimSpace(userID)
	tag = NormalizeVirtualTag(tag)
	if userID == "" || tag == "" {
		return userID
	}
	return userID + VirtualUserSep + tag
}

// SplitVirtualUserKey splits "userID@tag" into ("userID", "tag").
func SplitVirtualUserKey(key string) (userID, tag string) {
	key = strings.TrimSpace(key)
	idx := strings.LastIndex(key, VirtualUserSep)
	if idx <= 0 || idx == len(key)-1 {
		return key, ""
	}
	t := NormalizeVirtualTag(key[idx+1:])
	if t == "" {
		return key, ""
	}
	return strings.TrimSpace(key[:idx]), t
}

// DeriveVirtualPassword derives a deterministic, URL-safe 24-char password
// salted with inboundID and tag so unauthorized users cannot guess it.
func DeriveVirtualPassword(baseSecret, inboundID, tag string) string {
	baseSecret = strings.TrimSpace(baseSecret)
	inboundID = strings.TrimSpace(inboundID)
	tag = NormalizeVirtualTag(tag)
	if baseSecret == "" || tag == "" {
		return baseSecret
	}
	mac := hmac.New(sha256.New, []byte("liteplus:vinb:v1:"+inboundID+":"+tag))
	_, _ = mac.Write([]byte(baseSecret))
	sum := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(sum[:18])
}

// DeriveVirtualUUID derives a deterministic RFC 4122 UUIDv5 from baseUUID,
// inboundID, and tag so VLESS/VMess/TUIC receive a strictly valid 36-char UUID.
func DeriveVirtualUUID(baseUUID, inboundID, tag string) string {
	baseUUID = strings.TrimSpace(baseUUID)
	inboundID = strings.TrimSpace(inboundID)
	tag = NormalizeVirtualTag(tag)
	if baseUUID == "" || tag == "" {
		return baseUUID
	}
	h := sha1.New()
	_, _ = h.Write([]byte("liteplus:vinb:uuid:v1:"))
	_, _ = h.Write([]byte(inboundID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(tag))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(baseUUID))
	sum := h.Sum(nil)
	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50 // Version 5
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// DeriveVirtualUsername derives an HTTP/SOCKS username for a virtual tag.
func DeriveVirtualUsername(baseUser, tag string) string {
	baseUser = strings.TrimSpace(baseUser)
	tag = NormalizeVirtualTag(tag)
	if baseUser == "" || tag == "" {
		return baseUser
	}
	return baseUser + "-" + tag
}
