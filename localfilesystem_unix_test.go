//go:build unix

package fs

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalFileSystem_UserGroup(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Skipf("can't look up the current user: %s", err)
	}
	home := t.TempDir()
	file := filepath.Join(home, "file.txt")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	name, err := Local.User(file)
	require.NoError(t, err)
	assert.Equal(t, current.Username, name)
	require.NoError(t, Local.SetUser(file, current.Username), "SetUser to the owner")

	group, err := Local.Group(file)
	if err != nil {
		t.Skipf("can't look up the group of the file: %s", err)
	}
	require.NoError(t, Local.SetGroup(file, group), "SetGroup to the group of the file")

	// Only CleanPath expands the tilde, the methods take the path as it is
	t.Setenv(homeEnvVar(), home)
	t.Chdir(t.TempDir())
	tilde := "~/file.txt"
	_, err = Local.User(tilde)
	assert.ErrorIs(t, err, os.ErrNotExist, "User(%q)", tilde)
	_, err = Local.Group(tilde)
	assert.ErrorIs(t, err, os.ErrNotExist, "Group(%q)", tilde)
	assert.ErrorIs(t, Local.SetUser(tilde, current.Username), os.ErrNotExist, "SetUser(%q)", tilde)
	assert.ErrorIs(t, Local.SetGroup(tilde, group), os.ErrNotExist, "SetGroup(%q)", tilde)

	_, err = Local.User("")
	assert.ErrorIs(t, err, ErrEmptyPath)
	assert.ErrorIs(t, Local.SetGroup("", group), ErrEmptyPath)
}

func TestLocalFileSystem_WatchAddError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can watch every directory")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	require.NoError(t, os.Mkdir(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //#nosec G302 -- the directory must be removable again

	local := new(LocalFileSystem)
	t.Cleanup(func() { _ = local.Close() })
	_, err := local.Watch(dir, func(File, Event) {})
	require.ErrorIs(t, err, os.ErrPermission, "Watch of a directory without permissions")
	requireWatchGoroutines(t, local, 0, "a failed first Watch must not leave the watcher running")
}
