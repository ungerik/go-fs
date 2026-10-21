package fstest

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	fs "github.com/ungerik/go-fs"
)

// TestRunConformance_Reference runs the whole suite against the
// reference implementation of newMemMock, which follows the contract,
// so every failing check would be a bug of the suite.
func TestRunConformance_Reference(t *testing.T) {
	for _, sep := range []string{"/", `\`} {
		t.Run("separator "+sep, func(t *testing.T) {
			ref, testDir := newMemMock(sep)
			RunConformance(t, ref, Config{
				Name:    "MockFileSystem",
				Prefix:  ref.Prefix(),
				TestDir: testDir,
			})
		})
	}
}

// TestRunConformance_Seeds verifies that the suite derives the names it
// needs from the seed: the custom seed of docs/reference-fstest.md,
// FlatSeed and an empty seed pass on a writable file system.
func TestRunConformance_Seeds(t *testing.T) {
	for name, seed := range map[string]Seed{
		"FlatSeed": FlatSeed(),
		"Custom": {
			"a.txt":     []byte("a"),
			"dir/b.txt": []byte("b"),
		},
		"Empty": {},
		"OnlyNested": {
			"x/y/z.bin": {0, 1, 2},
		},
		"WorkDirName": {
			"fstest-work": []byte("the suite must use another work directory"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			ref, testDir := newMemMock("/")
			RunConformance(t, ref, Config{TestDir: testDir, Seed: seed})
		})
	}
}

// TestRunConformance_ReadOnly runs the suite against a read-only
// reference that already contains the seed.
func TestRunConformance_ReadOnly(t *testing.T) {
	ctx := t.Context()
	ref, testDir := newMemMock("/")
	seed := DefaultSeed()
	for _, name := range seed.files() {
		path := ref.CleanPath(testDir, name)
		mustNoError(t, ref.MakeAllDirs(parentPath(path), 0), "MakeAllDirs")
		mustNoError(t, ref.WriteAll(ctx, path, seed[name], 0), "WriteAll(%q)", path)
	}
	ref.MockReadableWritable = func() (bool, bool) { return true, false }

	RunConformance(t, ref, Config{TestDir: testDir, Seed: seed})
}

func parentPath(p string) string {
	return p[:strings.LastIndexByte(p, '/')]
}

// TestRunConformance_Skip verifies that Config.Skip gets the names
// relative to the calling test and skips the sub-tests below a skipped one.
func TestRunConformance_Skip(t *testing.T) {
	ref, testDir := newMemMock("/")
	var asked []string
	RunConformance(t, ref, Config{
		TestDir: testDir,
		Skip: func(name string) bool {
			asked = append(asked, name)
			return name != "WriteSeed" && name != "Rename" && name != "Rename/Overwrite"
		},
	})
	for _, name := range []string{"Metadata", "WriteSeed", "ReadErrors", "Rename", "Rename/Overwrite", "Rename/Self", "Close"} {
		checkTrue(t, slices.Contains(asked, name), "Skip must be asked for %q, was asked for %q", name, asked)
	}
	for _, name := range asked {
		checkFalse(t, strings.HasPrefix(name, "ReadErrors/"), "Skip must not be asked for %q below the skipped ReadErrors", name)
	}
}

// violations are variants of the reference file system that each break
// one rule of the contract, with the sub-test of the suite that must fail.
var violations = map[string]struct {
	subTest string
	apply   func(ref *MockFullyFeaturedFileSystem)
}{
	"ListDirMissingDirIsEmpty": {"ReadErrors/NotExist", func(ref *MockFullyFeaturedFileSystem) {
		base := *ref
		ref.MockListDir = func(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
			return fs.RemoveErrDoesNotExist(base.ListDir(ctx, dirPath, patterns, callback))
		}
	}},
	"URIWithoutBoundary": {"ListDir", func(ref *MockFullyFeaturedFileSystem) {
		base := *ref
		ref.MockListDir = func(ctx context.Context, dirPath string, patterns []string, callback func(*fs.FileInfo) error) error {
			return base.ListDir(ctx, dirPath, patterns, func(info *fs.FileInfo) error {
				info.File = fs.File(strings.Replace(string(info.File), ref.Prefix()+"/", ref.Prefix(), 1))
				return callback(info)
			})
		}
	}},
	"ReadAllIgnoresContext": {"Canceled/ReadAll", func(ref *MockFullyFeaturedFileSystem) {
		base := *ref
		ref.MockReadAll = func(_ context.Context, filePath string) ([]byte, error) {
			return base.ReadAll(context.Background(), filePath)
		}
	}},
	"RemoveNonEmptyUntyped": {"WriteErrors/RemoveNonEmptyDir", func(ref *MockFullyFeaturedFileSystem) {
		base := *ref
		ref.MockRemove = func(filePath string) error {
			err := base.Remove(filePath)
			if errors.Is(err, fs.ErrDirectoryNotEmpty{}) {
				return errors.New("directory not empty")
			}
			return err
		}
	}},
	"WriterIgnoresDirectory": {"WriteErrors/IsDirectory", func(ref *MockFullyFeaturedFileSystem) {
		base := *ref
		ref.MockOpenWriter = func(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
			w, err := base.OpenWriter(filePath, perm)
			if errors.Is(err, fs.ErrIsDirectory{}) {
				return nopWriteCloser{io.Discard}, nil
			}
			return w, err
		}
	}},
	"MoveMergesDirectories": {"MoveFileSystem/OntoDirectory", func(ref *MockFullyFeaturedFileSystem) {
		base := *ref
		ref.MockMove = func(ctx context.Context, filePath, destPath string) error {
			if info, err := ref.Stat(destPath); err == nil && info.IsDir {
				_, name := ref.pathHelper().SplitDirAndName(filePath)
				destPath = ref.CleanPath(destPath, name)
			}
			return base.Move(ctx, filePath, destPath)
		}
	}},
	"MoveIntoItselfUntyped": {"MoveFileSystem/IntoItself", func(ref *MockFullyFeaturedFileSystem) {
		base := *ref
		ref.MockMove = func(ctx context.Context, filePath, destPath string) error {
			if strings.HasPrefix(destPath, filePath+"/") {
				return errors.New("can't move into itself")
			}
			return base.Move(ctx, filePath, destPath)
		}
	}},
	"CopyIntoDirectory": {"CopyFileSystem/OntoDirectory", func(ref *MockFullyFeaturedFileSystem) {
		base := *ref
		ref.MockCopyFile = func(ctx context.Context, srcFile, destFile string) error {
			if info, err := ref.Stat(destFile); err == nil && info.IsDir {
				_, name := ref.pathHelper().SplitDirAndName(srcFile)
				destFile = ref.CleanPath(destFile, name)
			}
			return base.CopyFile(ctx, srcFile, destFile)
		}
	}},
	"AbortCommits": {"Abort/OpenWriter/NewFile", func(ref *MockFullyFeaturedFileSystem) {
		base := *ref
		ref.MockOpenWriter = func(filePath string, perm fs.Permissions) (io.WriteCloser, error) {
			w, err := base.OpenWriter(filePath, perm)
			if err != nil {
				return nil, err
			}
			return committingAborter{w}, nil
		}
	}},
	"StatAfterCloseNotExist": {"Close/Stat", func(ref *MockFullyFeaturedFileSystem) {
		base := *ref
		closed := false
		ref.MockClose = func() error {
			closed = true
			return base.MockClose()
		}
		ref.MockStat = func(filePath string) (*fs.FileInfo, error) {
			if closed {
				return nil, fs.NewErrDoesNotExist(ref.file(filePath))
			}
			return base.Stat(filePath)
		}
	}},
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// committingAborter commits the written data even when it is aborted.
type committingAborter struct{ io.WriteCloser }

func (w committingAborter) Abort() error { return w.Close() }

// onlyRun returns a Config.Skip function that runs only the sub-test
// target with its parents and children, and the seed setup.
func onlyRun(target string) func(string) bool {
	return func(name string) bool {
		run := name == "WriteSeed" ||
			name == target ||
			strings.HasPrefix(target, name+"/") ||
			strings.HasPrefix(name, target+"/")
		return !run
	}
}

// TestRunConformance_DetectsViolations runs the suite for every entry of
// violations in a subprocess, because the suite must fail there, and
// checks that the sub-test of the broken rule failed.
func TestRunConformance_DetectsViolations(t *testing.T) {
	for name, violation := range violations {
		t.Run(name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestViolationHelper$", "-test.v", "-test.count=1")
			cmd.Env = append(os.Environ(), "FSTEST_VIOLATION="+name)
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("the suite must fail for the violation %s, got error %v and output:\n%s", name, err, output)
			}
			failed := "--- FAIL: TestViolationHelper/" + violation.subTest + " "
			checkTrue(t, strings.Contains(string(output), failed), "the output must contain %q:\n%s", failed, output)
		})
	}
}

// TestViolationHelper runs the suite against a variant of the reference
// in the subprocess of TestRunConformance_DetectsViolations.
func TestViolationHelper(t *testing.T) {
	name := os.Getenv("FSTEST_VIOLATION")
	if name == "" {
		t.Skip("only runs in the subprocess of TestRunConformance_DetectsViolations")
	}
	violation, ok := violations[name]
	mustTrue(t, ok, "unknown violation %q", name)
	ref, testDir := newMemMock("/")
	violation.apply(ref)
	RunConformance(t, ref, Config{TestDir: testDir, Skip: onlyRun(violation.subTest)})
}
