package inboundcfg

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVirtualInboundHelpers(t *testing.T) {
	require.Equal(t, []string{"Home", "us_1"}, NormalizeVirtualTags([]string{" Home ", "home", "bad tag!", "us_1", ""}))

	require.Equal(t, "inb_1#home", FormatVirtualInboundID("inb_1", "home"))
	require.Equal(t, "inb_1", FormatVirtualInboundID("inb_1", ""))

	base, tag := SplitVirtualInboundID("inb_1#home")
	require.Equal(t, "inb_1", base)
	require.Equal(t, "home", tag)

	base, tag = SplitVirtualInboundID("inb_1")
	require.Equal(t, "inb_1", base)
	require.Equal(t, "", tag)

	require.Equal(t, "u1@home", VirtualUserKey("u1", "home"))
	uID, uTag := SplitVirtualUserKey("u1@home")
	require.Equal(t, "u1", uID)
	require.Equal(t, "home", uTag)
	uID, uTag = SplitVirtualUserKey("u1")
	require.Equal(t, "u1", uID)
	require.Equal(t, "", uTag)

	// Deterministic salted derivation
	u1 := DeriveVirtualUUID("11111111-1111-4111-8111-111111111111", "inb_1", "home")
	u2 := DeriveVirtualUUID("11111111-1111-4111-8111-111111111111", "inb_1", "home")
	u3 := DeriveVirtualUUID("11111111-1111-4111-8111-111111111111", "inb_1", "work")
	require.Equal(t, u1, u2)
	require.NotEqual(t, u1, u3)
	require.NotEqual(t, "11111111-1111-4111-8111-111111111111", u1)
	require.Len(t, u1, 36)
	require.Equal(t, byte('5'), u1[14]) // RFC 4122 v5

	p1 := DeriveVirtualPassword("secret123", "inb_1", "home")
	p2 := DeriveVirtualPassword("secret123", "inb_1", "home")
	p3 := DeriveVirtualPassword("secret123", "inb_2", "home")
	require.Equal(t, p1, p2)
	require.NotEqual(t, p1, p3)
	require.Len(t, p1, 24)
}
