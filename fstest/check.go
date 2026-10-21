package fstest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"testing"

	fs "github.com/ungerik/go-fs"
)

// The check functions report a failed check with t.Errorf and return
// whether the check passed, so callers can skip the checks that depend
// on it. The must functions stop the test with t.Fatalf instead.
// The format and args describe what was checked; the functions append
// what they got and what they expected.

func checkTrue(t testing.TB, cond bool, format string, args ...any) bool {
	t.Helper()
	if !cond {
		t.Errorf("%s", fmt.Sprintf(format, args...))
	}
	return cond
}

func checkFalse(t testing.TB, cond bool, format string, args ...any) bool {
	t.Helper()
	return checkTrue(t, !cond, format, args...)
}

func mustTrue(t testing.TB, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Fatalf("%s", fmt.Sprintf(format, args...))
	}
}

func checkNoError(t testing.TB, err error, format string, args ...any) bool {
	t.Helper()
	if err != nil {
		t.Errorf("%s: unexpected error %s", fmt.Sprintf(format, args...), describeErr(err))
		return false
	}
	return true
}

func mustNoError(t testing.TB, err error, format string, args ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error %s", fmt.Sprintf(format, args...), describeErr(err))
	}
}

func checkError(t testing.TB, err error, format string, args ...any) bool {
	t.Helper()
	if err == nil {
		t.Errorf("%s: expected an error, got nil", fmt.Sprintf(format, args...))
		return false
	}
	return true
}

// checkErrorIs checks errors.Is(err, target).
func checkErrorIs(t testing.TB, err, target error, format string, args ...any) bool {
	t.Helper()
	if err == nil {
		t.Errorf("%s: expected an error matching errors.Is(err, %s), got nil", fmt.Sprintf(format, args...), describeTarget(target))
		return false
	}
	if !errors.Is(err, target) {
		t.Errorf("%s: expected an error matching errors.Is(err, %s), got %s", fmt.Sprintf(format, args...), describeTarget(target), describeErr(err))
		return false
	}
	return true
}

func mustErrorIs(t testing.TB, err, target error, format string, args ...any) {
	t.Helper()
	if !checkErrorIs(t, err, target, format, args...) {
		t.FailNow()
	}
}

// checkNotErrorIs checks that a non-nil err does not match target.
func checkNotErrorIs(t testing.TB, err, target error, format string, args ...any) bool {
	t.Helper()
	if err != nil && errors.Is(err, target) {
		t.Errorf("%s: expected an error not matching errors.Is(err, %s), got %s", fmt.Sprintf(format, args...), describeTarget(target), describeErr(err))
		return false
	}
	return true
}

func checkEqual[T comparable](t testing.TB, want, got T, format string, args ...any) bool {
	t.Helper()
	if want != got {
		t.Errorf("%s:\n\twant %#v\n\t got %#v", fmt.Sprintf(format, args...), want, got)
		return false
	}
	return true
}

func mustEqual[T comparable](t testing.TB, want, got T, format string, args ...any) {
	t.Helper()
	if !checkEqual(t, want, got, format, args...) {
		t.FailNow()
	}
}

// checkBytes compares content. A nil and an empty slice are equal.
func checkBytes(t testing.TB, want, got []byte, format string, args ...any) bool {
	t.Helper()
	if bytes.Equal(want, got) {
		return true
	}
	if len(want) <= 64 && len(got) <= 64 {
		t.Errorf("%s:\n\twant %q\n\t got %q", fmt.Sprintf(format, args...), want, got)
		return false
	}
	diff := 0
	for diff < len(want) && diff < len(got) && want[diff] == got[diff] {
		diff++
	}
	t.Errorf("%s: want %d bytes, got %d bytes, first difference at offset %d", fmt.Sprintf(format, args...), len(want), len(got), diff)
	return false
}

func checkStrings(t testing.TB, want, got []string, format string, args ...any) bool {
	t.Helper()
	if len(want) == 0 && len(got) == 0 {
		return true
	}
	if !slices.Equal(want, got) {
		t.Errorf("%s:\n\twant %q\n\t got %q", fmt.Sprintf(format, args...), want, got)
		return false
	}
	return true
}

// checkSameFS checks that got is the file system instance want.
func checkSameFS(t testing.TB, want, got fs.FileSystem, format string, args ...any) bool {
	t.Helper()
	if !sameFS(want, got) {
		t.Errorf("%s:\n\twant %s (%T)\n\t got %s (%T)", fmt.Sprintf(format, args...), describeFS(want), want, describeFS(got), got)
		return false
	}
	return true
}

// sameFS compares two file systems by identity without
// panicking for values of non-comparable types.
func sameFS(a, b fs.FileSystem) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

func describeFS(fileSystem fs.FileSystem) string {
	if fileSystem == nil {
		return "<nil>"
	}
	return fileSystem.String()
}

// describeErr returns the message and the type of err.
func describeErr(err error) string {
	if err == nil {
		return "nil"
	}
	return fmt.Sprintf("%q (%T)", err.Error(), err)
}

// describeTarget returns the Go expression of an errors.Is target.
func describeTarget(target error) string {
	if target == nil {
		return "nil"
	}
	if reflect.TypeOf(target).Kind() == reflect.Struct {
		return fmt.Sprintf("%T{}", target) // Typed error like fs.ErrIsDirectory{}
	}
	switch target {
	case os.ErrNotExist:
		return "os.ErrNotExist"
	case os.ErrExist:
		return "os.ErrExist"
	case os.ErrInvalid:
		return "os.ErrInvalid"
	case os.ErrPermission:
		return "os.ErrPermission"
	case context.Canceled:
		return "context.Canceled"
	case io.EOF:
		return "io.EOF"
	case errors.ErrUnsupported:
		return "errors.ErrUnsupported"
	case fs.ErrFileSystemClosed:
		return "fs.ErrFileSystemClosed"
	case fs.ErrReadOnlyFileSystem:
		return "fs.ErrReadOnlyFileSystem"
	case fs.ErrWriteOnlyFileSystem:
		return "fs.ErrWriteOnlyFileSystem"
	}
	return describeErr(target)
}
