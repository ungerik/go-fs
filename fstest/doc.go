// Package fstest provides testing helpers for github.com/ungerik/go-fs,
// analogous to the standard library's testing/fstest package.
//
// RunConformance runs the conformance suite that every FileSystem
// implementation must pass. Every check of the suite is a named sub-test,
// like "ReadErrors/NotExist" or "Rename/Overwrite".
//
// # Compatibility
//
// The API of the package follows the go-fs version, but the suite is
// not frozen: minor versions of go-fs may add checks to RunConformance
// when the contract of fs.FileSystem is clarified or a gap in the suite
// is found, so a file system that passed may fail after an update.
// Use Config.Skip to opt out of a new check temporarily
// until the file system is fixed.
//
// The package only depends on the standard library and go-fs itself,
// so it adds no test dependencies to the modules that use it.
//
// # Mocks
//
// MockFileSystem and MockFullyFeaturedFileSystem implement the go-fs
// FileSystem interfaces with per-method function pointers, so tests can
// control individual behaviors without a real backing file system.
// MockFileSystem implements only fs.FileSystem and fs.WriteFileSystem,
// so the package emulates every optional interface for it.
// MockFullyFeaturedFileSystem implements every optional interface
// except fs.URIFileSystem, so the package always calls the mock.
package fstest
