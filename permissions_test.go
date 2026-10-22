package fs

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPermissions_FileModeRoundTrip checks that FileMode maps the setuid,
// setgid and sticky bits to the os.FileMode bits and PermissionsFromFileMode
// maps them back, so the special bits survive a round trip.
func TestPermissions_FileModeRoundTrip(t *testing.T) {
	tests := []struct {
		perm Permissions
		mode os.FileMode
	}{
		{perm: 0o644, mode: 0o644},
		{perm: 0o4755, mode: os.ModeSetuid | 0o755},
		{perm: 0o2755, mode: os.ModeSetgid | 0o755},
		{perm: 0o1777, mode: os.ModeSticky | 0o777},
		{perm: 0o7777, mode: os.ModeSetuid | os.ModeSetgid | os.ModeSticky | 0o777},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%04o", uint32(tt.perm)), func(t *testing.T) {
			require.Equal(t, tt.mode, tt.perm.FileMode(false))
			require.Equal(t, tt.mode|os.ModeDir, tt.perm.FileMode(true))
			require.Equal(t, tt.perm, PermissionsFromFileMode(tt.mode))
			require.Equal(t, tt.perm, PermissionsFromFileMode(tt.mode|os.ModeDir), "the file type bits are dropped")
			require.Equal(t, tt.perm, PermissionsFromFileMode(tt.perm.FileMode(true)), "round trip")
		})
	}
}

func TestPermissions_Can(t *testing.T) {
	perm := UserReadWrite | GroupRead
	require.True(t, perm.Can(UserRead))
	require.True(t, perm.Can(UserReadWrite), "all bits of p are set")
	require.True(t, perm.Can(NoPermissions), "everything grants nothing")
	require.False(t, perm.Can(UserExecute))
	require.False(t, perm.Can(UserRead|OthersRead), "one bit of p is missing")
}

func TestPermissions_ReadableWritableExecutable(t *testing.T) {
	tests := []struct {
		perm                      Permissions
		readable, writable, execs [3]bool // user, group, others
	}{
		{perm: NoPermissions},
		{perm: 0o754, readable: [3]bool{true, true, true}, writable: [3]bool{true, false, false}, execs: [3]bool{true, true, false}},
		{perm: AllRead, readable: [3]bool{true, true, true}},
		{perm: AllWrite, writable: [3]bool{true, true, true}},
		{perm: AllExecute, execs: [3]bool{true, true, true}},
		{perm: 0o7000}, // The special bits grant nothing
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%04o", uint32(tt.perm)), func(t *testing.T) {
			user, group, others := tt.perm.Readable()
			require.Equal(t, tt.readable, [3]bool{user, group, others}, "Readable")
			user, group, others = tt.perm.Writable()
			require.Equal(t, tt.writable, [3]bool{user, group, others}, "Writable")
			user, group, others = tt.perm.Executable()
			require.Equal(t, tt.execs, [3]bool{user, group, others}, "Executable")
		})
	}
}
