package fs_test

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/fstest"
)

// minimalFileSystem implements nothing but fs.FileSystem and
// fs.WriteFileSystem by forwarding to a MemFileSystem. Every other
// operation therefore has to be served by the generic emulations in
// dispatch.go instead of a native implementation.
//
// The methods are written out instead of embedding the MemFileSystem,
// because embedding would promote all of its optional interfaces and
// bypass exactly the emulations this file system exists to exercise.
type minimalFileSystem struct {
	fsimpl.PathHelper

	mem *fs.MemFileSystem
}

var (
	_ fs.FileSystem      = new(minimalFileSystem)
	_ fs.WriteFileSystem = new(minimalFileSystem)
)

func newMinimalFileSystem(t *testing.T) *minimalFileSystem {
	t.Helper()
	mem, err := fs.NewMemFileSystem("/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	return &minimalFileSystem{
		PathHelper: fsimpl.PathHelper{URIPrefix: "minimal://emulated", Rooted: true},
		mem:        mem,
	}
}

func (m *minimalFileSystem) ID() string     { return "emulated" }
func (m *minimalFileSystem) Name() string   { return "minimal file system" }
func (m *minimalFileSystem) String() string { return m.Name() + " with prefix " + m.Prefix() }
func (m *minimalFileSystem) RootDir() fs.File {
	return fs.File(m.URL(m.Separator()))
}

func (m *minimalFileSystem) ReadableWritable() (readable, writable bool) {
	return m.mem.ReadableWritable()
}

// info returns a copy of the MemFileSystem info with the File
// translated from the mem:// URI to this file system.
func (m *minimalFileSystem) info(filePath string, memInfo *fs.FileInfo) *fs.FileInfo {
	info := *memInfo
	info.File = fs.File(m.JoinCleanURI(filePath))
	return &info
}

func (m *minimalFileSystem) Stat(filePath string) (*fs.FileInfo, error) {
	memInfo, err := m.mem.Stat(filePath)
	if err != nil {
		return nil, err
	}
	return m.info(filePath, memInfo), nil
}

func (m *minimalFileSystem) ListDir(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
	return m.mem.ListDir(ctx, dirPath, patterns, func(memInfo *fs.FileInfo) error {
		return callback(m.info(m.CleanPath(dirPath, memInfo.Name), memInfo))
	})
}

func (m *minimalFileSystem) OpenReader(filePath string) (io.ReadCloser, error) {
	return m.mem.OpenReader(filePath)
}

func (m *minimalFileSystem) OpenWriter(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
	return m.mem.OpenWriter(filePath, perm)
}

func (m *minimalFileSystem) MakeDir(dirPath string, perm fs.Permissions) error {
	return m.mem.MakeDir(dirPath, perm)
}

func (m *minimalFileSystem) Remove(filePath string) error {
	return m.mem.Remove(filePath)
}

func (m *minimalFileSystem) Close() error {
	fs.Unregister(m)
	return nil
}

// TestGenericEmulations_Conformance runs the conformance suite against a
// file system that implements only the required primitives, so that the
// suite exercises the generic emulations of dispatch.go: Exists, ReadAll,
// WriteAll, Append, OpenAppendWriter, OpenReadWriter, Truncate, Touch,
// MakeAllDirs, RemoveAll, Move, Rename, ListDirMax and ListDirRecursive.
//
// Every other backend of this module implements those optional
// interfaces natively, so without this test the emulations that make the
// interfaces optional in the first place would go unverified.
func TestGenericEmulations_Conformance(t *testing.T) {
	minimal := newMinimalFileSystem(t)
	require.NoError(t, minimal.MakeDir("/conformance", 0), "creating the test directory")

	fstest.RunConformance(t, minimal, fstest.Config{
		Name:    "minimal file system",
		Prefix:  "minimal://emulated",
		TestDir: "/conformance",
	})
}
