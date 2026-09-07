package fs_test

// This file holds the File tests that rely on the fstest mock file system.
// It lives in the external fs_test package (with a dot import of fs) because
// fstest imports fs, so an internal package fs test file importing fstest
// would create an import cycle.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	. "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
	"github.com/ungerik/go-fs/fstest"
)

// TestFile comprehensively tests File methods using MockFileSystem with different Permissions
func TestFile(t *testing.T) {
	// Helper to create a mock file system with minimal setup.
	//
	// MockFullyFeaturedFileSystem implements every optional interface and the
	// fs package dispatches to an optional interface whenever it is implemented,
	// so every hook a File method can reach has to be set here or in the test.
	// The fs package always hands the mock cleaned paths ("/a/b" form, prefix stripped).
	createMockFS := func(prefix string) *fstest.MockFullyFeaturedFileSystem {
		mockFS := &fstest.MockFullyFeaturedFileSystem{}
		mockFS.MockFileSystem = fstest.MockFileSystem{
			MockPrefix: prefix,
			MockReadableWritable: func() (bool, bool) {
				return true, true // Mock filesystem is both readable and writable
			},
			MockStat: func(filePath string) (*FileInfo, error) {
				// Default implementation that returns an existing regular file.
				// File is left empty so the fs package fills it in.
				return &FileInfo{
					Name:        path.Base(filePath),
					Exists:      true,
					IsRegular:   true,
					Size:        100,
					Permissions: 0644,
				}, nil
			},
			MockListDir: func(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
				// Default implementation that returns empty directory
				return nil
			},
			MockOpenReader: func(filePath string) (io.ReadCloser, error) {
				// Return a reader with the content of MockReadAll (empty by default)
				data, err := mockFS.MockReadAll(t.Context(), filePath)
				if err != nil {
					return nil, err
				}
				return io.NopCloser(bytes.NewReader(data)), nil
			},
			MockMakeDir: func(dirPath string, perm Permissions) error {
				return nil
			},
			MockRemove: func(filePath string) error {
				return nil
			},
			MockRootDir: func() File {
				return File(prefix)
			},
		}
		mockFS.MockExists = func(filePath string) (bool, error) {
			_, err := mockFS.MockStat(filePath)
			switch {
			case err == nil:
				return true, nil
			case errors.Is(err, os.ErrNotExist):
				return false, nil
			default:
				return false, err
			}
		}
		mockFS.MockOpenAppendWriter = func(filePath string, perm Permissions) (WriteCloser, error) {
			// Emulate the fallback implementation: read existing content,
			// return a buffer that calls WriteAll on close
			current, err := mockFS.MockReadAll(t.Context(), filePath)
			if err != nil {
				current = []byte{} // Empty if file doesn't exist
			}
			return fsimpl.NewWriteOnCloseFileBuffer(current, func(data []byte) error {
				return mockFS.MockWriteAll(t.Context(), filePath, data, perm)
			}), nil
		}
		mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
			// Default implementation that returns empty data
			return []byte{}, nil
		}
		mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm Permissions) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockListDirMax = func(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
			// Default implementation that returns empty list
			return []File{}, nil
		}
		mockFS.MockListDirRecursive = func(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
			// Default implementation that returns empty directory
			return nil
		}
		mockFS.MockVolumeName = func(filePath string) string {
			// Mock file systems don't have volumes
			return ""
		}
		mockFS.MockAbsPath = func(filePath string) string {
			// For mock purposes, just ensure it starts with / (but only add if not present)
			if !strings.HasPrefix(filePath, "/") {
				return "/" + filePath
			}
			return filePath
		}
		mockFS.MockIsAbsPath = func(filePath string) bool {
			return strings.HasPrefix(filePath, "/")
		}
		mockFS.MockIsHidden = func(filePath string) bool {
			// Check if filename starts with dot
			return strings.HasPrefix(path.Base(filePath), ".")
		}
		mockFS.MockIsSymbolicLink = func(filePath string) bool {
			// Mock filesystem doesn't support symbolic links
			return false
		}
		mockFS.MockSetPermissions = func(filePath string, perm Permissions) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockUser = func(filePath string) (string, error) {
			// Default implementation that returns a test user
			return "testuser", nil
		}
		mockFS.MockSetUser = func(filePath string, user string) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockGroup = func(filePath string) (string, error) {
			// Default implementation that returns a test group
			return "testgroup", nil
		}
		mockFS.MockSetGroup = func(filePath string, group string) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockTouch = func(filePath string, perm Permissions) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockWatch = func(filePath string, onEvent func(File, Event)) (cancel func() error, err error) {
			// Default implementation that returns a no-op cancel function
			return func() error { return nil }, nil
		}
		mockFS.MockTruncate = func(filePath string, size int64) error {
			// Default implementation that does nothing
			return nil
		}
		mockFS.MockRename = func(filePath string, newName string) (newPath string, err error) {
			// Default implementation that returns a new path
			return path.Join(path.Dir(filePath), newName), nil
		}
		mockFS.MockMove = func(filePath string, destinationPath string) error {
			// Default implementation that does nothing
			return nil
		}
		return mockFS
	}

	// Test different permission combinations for each method.
	// The file system receives a single Permissions value:
	// all passed permissions OR-ed together, or zero (NoPermissions)
	// meaning the default of the file system when none are passed.
	permissionTests := []struct {
		name        string
		permissions []Permissions
		want        Permissions
	}{
		{"NoPermissions", []Permissions{}, NoPermissions},
		{"SinglePermission", []Permissions{0644}, 0644},
		{"MultiplePermissions", []Permissions{0644, 0755}, 0644 | 0755},
		{"ReadOnly", []Permissions{0444}, 0444},
		{"WriteOnly", []Permissions{0222}, 0222},
		{"ExecuteOnly", []Permissions{0111}, 0111},
		{"FullPermissions", []Permissions{0777}, 0777},
		{"UserReadWrite", []Permissions{0600}, 0600},
		{"GroupReadWrite", []Permissions{0660}, 0660},
		{"OtherReadWrite", []Permissions{0606}, 0606},
		{"StickyBit", []Permissions{01777}, 01777},
		{"SetUID", []Permissions{04755}, 04755},
		{"SetGID", []Permissions{02755}, 02755},
	}

	t.Run("MakeDir", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				// MakeDir stats first and is a no-op for an existing directory
				// (or an error for an existing file), so the path must not exist
				mockFS.MockStat = func(filePath string) (*FileInfo, error) {
					return nil, os.ErrNotExist
				}

				var capturedPerm Permissions
				mockFS.MockMakeDir = func(dirPath string, perm Permissions) error {
					capturedPerm = perm
					assert.Equal(t, "/test/path/to/file.txt", dirPath)
					return nil
				}

				err := file.MakeDir(permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("MakeAllDirs", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				// MockFullyFeaturedFileSystem implements MakeAllDirsFileSystem,
				// so the permissions are passed to the native MakeAllDirs
				// instead of the per-directory MakeDir emulation.
				var capturedPerm Permissions
				mockFS.MockMakeAllDirs = func(dirPath string, perm Permissions) error {
					capturedPerm = perm
					assert.Equal(t, "/test/path/to/file.txt", dirPath)
					return nil
				}

				err := file.MakeAllDirs(permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("OpenWriter", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				var capturedPerm Permissions
				mockFS.MockOpenWriter = func(filePath string, perm Permissions) (WriteCloser, error) {
					capturedPerm = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					return &fsimpl.FileBuffer{}, nil
				}

				writer, err := file.OpenWriter(permTest.permissions...)
				require.NoError(t, err)
				require.NotNil(t, writer)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("OpenAppendWriter", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				// Mock ReadAll to return existing content
				mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
					return []byte("existing content"), nil
				}

				var capturedPerm Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm Permissions) error {
					capturedPerm = perm
					return nil
				}

				writer, err := file.OpenAppendWriter(permTest.permissions...)
				require.NoError(t, err)
				require.NotNil(t, writer)

				// Close to trigger WriteAll
				err = writer.Close()
				require.NoError(t, err)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("OpenReadWriter", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				var capturedPerm Permissions
				mockFS.MockOpenReadWriter = func(filePath string, perm Permissions) (ReadWriteSeekCloser, error) {
					capturedPerm = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					return &fsimpl.FileBuffer{}, nil
				}

				readWriter, err := file.OpenReadWriter(permTest.permissions...)
				require.NoError(t, err)
				require.NotNil(t, readWriter)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("WriteAll", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testData := []byte("test content")
				var capturedPerm Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm Permissions) error {
					capturedPerm = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					require.Equal(t, testData, data)
					return nil
				}

				err := file.WriteAll(t.Context(), testData, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("WriteAllContext", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testData := []byte("test content")
				var capturedPerm Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm Permissions) error {
					capturedPerm = perm
					require.Equal(t, "/test/path/to/file.txt", filePath)
					require.Equal(t, testData, data)
					return nil
				}

				err := file.WriteAll(t.Context(), testData, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("WriteAllString", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testStr := "test content"
				var capturedPerm Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm Permissions) error {
					capturedPerm = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					assert.Equal(t, []byte(testStr), data)
					return nil
				}

				err := file.WriteAllString(t.Context(), testStr, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("WriteAllStringContext", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testStr := "test content"
				var capturedPerm Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm Permissions) error {
					capturedPerm = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					assert.Equal(t, []byte(testStr), data)
					return nil
				}

				err := file.WriteAllString(t.Context(), testStr, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("Append", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testData := []byte("appended content")
				var capturedPerm Permissions
				mockFS.MockAppend = func(ctx context.Context, filePath string, data []byte, perm Permissions) error {
					capturedPerm = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					require.Equal(t, testData, data)
					return nil
				}

				err := file.Append(t.Context(), testData, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("AppendString", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testStr := "appended content"
				var capturedPerm Permissions
				mockFS.MockAppend = func(ctx context.Context, filePath string, data []byte, perm Permissions) error {
					capturedPerm = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					assert.Equal(t, []byte(testStr), data)
					return nil
				}

				err := file.AppendString(t.Context(), testStr, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.want, capturedPerm)
			})
		}
	})

	t.Run("WriteJSON", func(t *testing.T) {
		// WriteJSON doesn't accept permissions, so we just test it once
		// Create mock file system for this test with only needed functions
		mockFS := createMockFS("mock" + t.Name() + "://")
		Register(mockFS)
		t.Cleanup(func() { Unregister(mockFS) })

		file := File("mock" + t.Name() + "://test/path/to/file.txt")

		testData := map[string]any{"name": "test", "value": 123}
		capturedPerm := Permissions(0777) // Sentinel that must be overwritten
		mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm Permissions) error {
			capturedPerm = perm
			assert.Equal(t, "/test/path/to/file.txt", filePath)
			// Verify it's valid JSON
			assert.Contains(t, string(data), `"name":"test"`)
			assert.Contains(t, string(data), `"value":123`)
			return nil
		}

		err := file.WriteJSON(t.Context(), testData)
		require.NoError(t, err)
		// WriteJSON doesn't support permissions, so it should pass the default
		require.Equal(t, NoPermissions, capturedPerm)
	})

	t.Run("WriteXML", func(t *testing.T) {
		// WriteXML doesn't accept permissions, so we just test it once
		// Create mock file system for this test with only needed functions
		mockFS := createMockFS("mock" + t.Name() + "://")
		Register(mockFS)
		t.Cleanup(func() { Unregister(mockFS) })

		file := File("mock" + t.Name() + "://test/path/to/file.txt")

		testData := struct {
			XMLName struct{} `xml:"root"`
			Name    string   `xml:"name"`
			Value   int      `xml:"value"`
		}{Name: "test", Value: 123}

		capturedPerm := Permissions(0777) // Sentinel that must be overwritten
		mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm Permissions) error {
			capturedPerm = perm
			assert.Equal(t, "/test/path/to/file.txt", filePath)
			// Verify it's valid XML with header
			assert.Contains(t, string(data), `<?xml version="1.0" encoding="UTF-8"?>`)
			assert.Contains(t, string(data), `<name>test</name>`)
			assert.Contains(t, string(data), `<value>123</value>`)
			return nil
		}

		err := file.WriteXML(t.Context(), testData)
		require.NoError(t, err)
		// WriteXML doesn't support permissions, so it should pass the default
		require.Equal(t, NoPermissions, capturedPerm)
	})

	t.Run("ReadFrom", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testReader := strings.NewReader("test content")

				// Mock Stat to return existing file with permissions
				mockFS.MockStat = func(filePath string) (*FileInfo, error) {
					return &FileInfo{
						Name:        "file.txt",
						Exists:      true,
						IsRegular:   true,
						Size:        0,
						Permissions: 0644,
					}, nil
				}

				var capturedPerm Permissions
				mockFS.MockOpenWriter = func(filePath string, perm Permissions) (WriteCloser, error) {
					capturedPerm = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					return &fsimpl.FileBuffer{}, nil
				}

				n, err := file.ReadFrom(testReader)
				require.NoError(t, err)
				assert.Equal(t, int64(12), n) // "test content" length
				// ReadFrom should use existing file permissions, not the test permissions
				assert.Equal(t, Permissions(0644), capturedPerm)
			})
		}
	})

	// Test methods that don't take permissions but should still work
	t.Run("NonPermissionMethods", func(t *testing.T) {
		// Create a shared mock file system for all NonPermissionMethods subtests
		mockFS := createMockFS("mock" + t.Name() + "://")
		Register(mockFS)
		t.Cleanup(func() { Unregister(mockFS) })

		file := File("mock" + t.Name() + "://test/path/to/file.txt")

		t.Run("FileSystem", func(t *testing.T) {
			fs := file.FileSystem()
			require.Equal(t, mockFS, fs)
		})

		t.Run("ParseRawURI", func(t *testing.T) {
			fs, path := file.ParseRawURI()
			require.Equal(t, mockFS, fs)
			assert.Equal(t, "/test/path/to/file.txt", path)
		})

		t.Run("RawURI", func(t *testing.T) {
			uri := file.RawURI()
			assert.Equal(t, string(file), uri)
		})

		t.Run("String", func(t *testing.T) {
			str := file.String()
			assert.Equal(t, string(file), str)
		})

		t.Run("URL", func(t *testing.T) {
			url := file.URL()
			assert.Equal(t, string(file), url)
		})

		t.Run("Path", func(t *testing.T) {
			path := file.Path()
			assert.Equal(t, "/test/path/to/file.txt", path)
		})

		t.Run("PathWithSlashes", func(t *testing.T) {
			path := file.PathWithSlashes()
			assert.Equal(t, "/test/path/to/file.txt", path)
		})

		t.Run("LocalPath", func(t *testing.T) {
			localPath := file.LocalPath()
			assert.Equal(t, "", localPath) // Not a local file system
		})

		t.Run("MustLocalPath", func(t *testing.T) {
			// Should panic for non-local file system
			assert.Panics(t, func() {
				file.MustLocalPath()
			})
		})

		t.Run("Name", func(t *testing.T) {
			name := file.Name()
			assert.Equal(t, "file.txt", name)
		})

		t.Run("Dir", func(t *testing.T) {
			dir := file.Dir()
			require.Equal(t, file.Dir(), dir) // Just verify it works consistently
		})

		t.Run("DirAndName", func(t *testing.T) {
			dir, name := file.DirAndName()
			assert.Equal(t, file.Dir(), dir)
			assert.Equal(t, "file.txt", name)
		})

		t.Run("VolumeName", func(t *testing.T) {
			volume := file.VolumeName()
			assert.Equal(t, "", volume) // MockVolumeName reports no volume
		})

		t.Run("Ext", func(t *testing.T) {
			ext := file.Ext()
			assert.Equal(t, ".txt", ext)
		})

		t.Run("ExtLower", func(t *testing.T) {
			file := File("mock://test/path/to/FILE.TXT")
			ext := file.ExtLower()
			assert.Equal(t, ".txt", ext)
		})

		t.Run("TrimExt", func(t *testing.T) {
			trimmed := file.TrimExt()
			assert.True(t, strings.HasSuffix(string(trimmed), "://test/path/to/file"))
		})

		t.Run("Join", func(t *testing.T) {
			joined := file.Join("subdir", "nested.txt")
			assert.True(t, strings.HasSuffix(string(joined), "file.txt/subdir/nested.txt"))
		})

		t.Run("Joinf", func(t *testing.T) {
			joined := file.Joinf("file_%d.txt", 123)
			assert.True(t, strings.HasSuffix(string(joined), "file.txt/file_123.txt"))
		})

		t.Run("IsReadable", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Mock Stat to return a readable file
			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{
					Name:        "file.txt",
					Exists:      true,
					IsRegular:   true,
					Size:        100,
					Permissions: 0644,
				}, nil
			}

			readable := file.IsReadable()
			require.True(t, readable)
		})

		t.Run("IsWritable", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Mock Stat to return a writable file
			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{
					Name:        "file.txt",
					Exists:      true,
					IsRegular:   true,
					Size:        100,
					Permissions: 0644,
				}, nil
			}

			writable := file.IsWritable()
			require.True(t, writable)
		})

		t.Run("Stat", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedInfo := &FileInfo{
				Name:        "file.txt",
				Exists:      true,
				IsRegular:   true,
				Size:        100,
				Permissions: 0644,
			}

			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return expectedInfo, nil
			}

			// Stat returns the io/fs.FileInfo view of the FileInfo returned by the file system
			info, err := file.Stat()
			require.NoError(t, err)
			assert.Equal(t, "file.txt", info.Name())
			assert.Equal(t, int64(100), info.Size())
			assert.Equal(t, os.FileMode(0644), info.Mode())
			assert.False(t, info.IsDir())
		})

		t.Run("Info", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedInfo := &FileInfo{
				Name:        "file.txt",
				Exists:      true,
				IsRegular:   true,
				Size:        100,
				Permissions: 0644,
			}

			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return expectedInfo, nil
			}

			info := file.Info()
			require.NotNil(t, info)
			assert.Equal(t, file, info.File) // Filled in by the fs package from the Name
		})

		t.Run("Exists", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Mock Stat to return no error (file exists)
			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{Name: "file.txt", Exists: true}, nil
			}

			exists := file.Exists()
			require.True(t, exists)
		})

		t.Run("CheckExists", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Test with existing file
			testMockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{Name: "file.txt", Exists: true}, nil
			}

			err := testFile.CheckExists()
			require.NoError(t, err)

			// Test with non-existing file
			testMockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return nil, os.ErrNotExist
			}

			err = testFile.CheckExists()
			require.Error(t, err)
			require.IsType(t, ErrDoesNotExist{}, err)
		})

		t.Run("IsDir", func(t *testing.T) {
			// Use the shared mockFS and override MockStat temporarily
			originalMockStat := mockFS.MockStat
			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{
					Name:   "file.txt",
					Exists: true,
					IsDir:  true,
				}, nil
			}
			defer func() { mockFS.MockStat = originalMockStat }()

			isDir := file.IsDir()
			require.True(t, isDir)
		})

		t.Run("CheckIsDir", func(t *testing.T) {
			// Use the shared mockFS and override MockStat temporarily
			originalMockStat := mockFS.MockStat

			// Test with directory
			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{
					Name:   "file.txt",
					Exists: true,
					IsDir:  true,
				}, nil
			}

			err := file.CheckIsDir()
			require.NoError(t, err)

			// Test with file (not directory)
			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{
					Name:      "file.txt",
					Exists:    true,
					IsRegular: true,
				}, nil
			}

			err = file.CheckIsDir()
			require.Error(t, err)
			require.IsType(t, ErrIsNotDirectory{}, err)

			mockFS.MockStat = originalMockStat
		})

		t.Run("AbsPath", func(t *testing.T) {
			absPath := file.AbsPath()
			assert.Equal(t, "/test/path/to/file.txt", absPath)
		})

		t.Run("HasAbsPath", func(t *testing.T) {
			hasAbs := file.HasAbsPath()
			assert.True(t, hasAbs) // path starts with /
		})

		t.Run("ToAbsPath", func(t *testing.T) {
			absFile := file.ToAbsPath()
			assert.Equal(t, file, absFile) // Already absolute
		})

		t.Run("IsRegular", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Create file with matching prefix
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Mock Stat to return a regular file
			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{
					Name:        "file.txt",
					Exists:      true,
					IsRegular:   true,
					Permissions: 0644,
				}, nil
			}

			isRegular := file.IsRegular()
			assert.True(t, isRegular)
		})

		t.Run("IsEmptyDir", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Create file with matching prefix
			file := File("mock" + t.Name() + "://test/path/to/dir")

			// Mock ListDirMax to return empty list
			mockFS.MockListDirMax = func(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
				return nil, nil
			}

			isEmpty := file.IsEmptyDir()
			assert.True(t, isEmpty)
		})

		t.Run("IsHidden", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFullyFeaturedFileSystem implements HiddenFileSystem,
			// MockIsHidden uses the dot rule
			hiddenFile := File("mock" + t.Name() + "://test/path/to/.hidden")
			hidden := hiddenFile.IsHidden()
			assert.True(t, hidden)
		})

		t.Run("IsSymbolicLink", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			symlink := file.IsSymbolicLink()
			assert.False(t, symlink) // MockIsSymbolicLink returns false
		})

		t.Run("Size", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Mock Stat to return a file with size
			testMockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{
					Name:   "file.txt",
					Exists: true,
					Size:   1024,
				}, nil
			}

			size := testFile.Size()
			assert.Equal(t, int64(1024), size)
		})

		t.Run("ContentHash", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Mock OpenReader to return a reader with content
			mockFS.MockOpenReader = func(filePath string) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("test content")), nil
			}

			hash, err := file.ContentHash(t.Context())
			require.NoError(t, err)
			assert.NotEmpty(t, hash)
		})

		t.Run("ContentHashContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			// Mock OpenReader to return a reader with content
			mockFS.MockOpenReader = func(filePath string) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("test content")), nil
			}

			hash, err := file.ContentHash(t.Context())
			require.NoError(t, err)
			assert.NotEmpty(t, hash)
		})

		t.Run("Modified", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedTime := time.Now()
			testMockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{
					Name:     "file.txt",
					Exists:   true,
					Modified: expectedTime,
				}, nil
			}

			modified := testFile.Modified()
			assert.Equal(t, expectedTime, modified)
		})

		t.Run("Permissions", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{
					Name:        "file.txt",
					Exists:      true,
					Permissions: 0644,
				}, nil
			}

			perm := file.Permissions()
			assert.Equal(t, Permissions(0644), perm)
		})

		t.Run("SetPermissions", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// fstest.MockFullyFeaturedFileSystem implements SetPermissions
			err := testFile.SetPermissions(0644)
			require.NoError(t, err)
		})

		t.Run("ListDir", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/dir")

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			testMockFS.MockListDir = func(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
				assert.Equal(t, "/test/path/to/dir", dirPath)
				for _, f := range expectedFiles {
					info := &FileInfo{File: f, Name: f.Name(), Exists: true, IsRegular: true}
					if err := callback(info); err != nil {
						return err
					}
				}
				return nil
			}

			var listedFiles []File
			err := testFile.ListDir(t.Context(), func(f File) error {
				listedFiles = append(listedFiles, f)
				return nil
			})

			require.NoError(t, err)
			assert.Equal(t, expectedFiles, listedFiles)
		})

		t.Run("ListDirContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/dir")

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			testMockFS.MockListDir = func(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
				for _, f := range expectedFiles {
					info := &FileInfo{File: f, Name: f.Name(), Exists: true, IsRegular: true}
					if err := callback(info); err != nil {
						return err
					}
				}
				return nil
			}

			var listedFiles []File
			err := testFile.ListDir(t.Context(), func(f File) error {
				listedFiles = append(listedFiles, f)
				return nil
			})

			require.NoError(t, err)
			assert.Equal(t, expectedFiles, listedFiles)
		})

		t.Run("ListDirIter", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/dir")

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			testMockFS.MockListDir = func(ctx context.Context, dirPath string, patterns []string, callback func(*FileInfo) error) error {
				for _, f := range expectedFiles {
					info := &FileInfo{File: f, Name: f.Name(), Exists: true, IsRegular: true}
					if err := callback(info); err != nil {
						return err
					}
				}
				return nil
			}

			var listedFiles []File
			for f, err := range testFile.ListDirIter(t.Context()) {
				require.NoError(t, err)
				listedFiles = append(listedFiles, f)
			}

			assert.Equal(t, expectedFiles, listedFiles)
		})

		t.Run("ListDirMax", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/dir")

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			testMockFS.MockListDirMax = func(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
				return expectedFiles, nil
			}

			files, err := testFile.ListDirMax(t.Context(), 10)
			require.NoError(t, err)
			assert.Equal(t, expectedFiles, files)
		})

		t.Run("User", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// fstest.MockFullyFeaturedFileSystem implements User methods
			user, err := testFile.User()
			require.NoError(t, err)
			assert.Equal(t, "testuser", user)
		})

		t.Run("SetUser", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// fstest.MockFullyFeaturedFileSystem implements SetUser
			err := testFile.SetUser("testuser")
			require.NoError(t, err)
		})

		t.Run("Group", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// fstest.MockFullyFeaturedFileSystem implements Group
			group, err := testFile.Group()
			require.NoError(t, err)
			assert.Equal(t, "testgroup", group)
		})

		t.Run("SetGroup", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// fstest.MockFullyFeaturedFileSystem implements SetGroup
			err := testFile.SetGroup("testgroup")
			require.NoError(t, err)
		})

		t.Run("Touch", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// fstest.MockFullyFeaturedFileSystem implements Touch
			err := testFile.Touch()
			require.NoError(t, err)
		})

		t.Run("WriteTo", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			testMockFS.MockOpenReader = func(filePath string) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("test content")), nil
			}

			var buf bytes.Buffer
			n, err := testFile.WriteTo(&buf)
			require.NoError(t, err)
			assert.Equal(t, int64(12), n) // "test content" length
			assert.Equal(t, "test content", buf.String())
		})

		t.Run("OpenReader", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			testMockFS.MockOpenReader = func(filePath string) (io.ReadCloser, error) {
				assert.Equal(t, "/test/path/to/file.txt", filePath)
				return io.NopCloser(strings.NewReader("test content")), nil
			}

			// An io.ReadCloser without a Stat method is wrapped
			// so that the result implements io/fs.File with Stat
			// backed by File.Stat.
			reader, err := testFile.OpenReader()
			require.NoError(t, err)
			data, err := io.ReadAll(reader)
			require.NoError(t, err)
			assert.Equal(t, "test content", string(data))
			info, err := reader.Stat()
			require.NoError(t, err)
			assert.Equal(t, "file.txt", info.Name())
			require.NoError(t, reader.Close())
		})

		t.Run("OpenReadSeeker", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			mockFS.MockOpenReader = func(filePath string) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("test content")), nil
			}

			mockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{
					Name:   "file.txt",
					Exists: true,
					Size:   12,
				}, nil
			}

			reader, err := file.OpenReadSeeker()
			require.NoError(t, err)
			require.NotNil(t, reader)
		})

		t.Run("ReadAll", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedData := []byte("test content")
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			data, err := testFile.ReadAll(t.Context())
			require.NoError(t, err)
			assert.Equal(t, expectedData, data)
		})

		t.Run("ReadAllContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedData := []byte("test content")
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			data, err := testFile.ReadAll(t.Context())
			require.NoError(t, err)
			assert.Equal(t, expectedData, data)
		})

		t.Run("ReadAllContentHash", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedData := []byte("test content")
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			data, hash, err := testFile.ReadAllContentHash(t.Context())
			require.NoError(t, err)
			assert.Equal(t, expectedData, data)
			assert.NotEmpty(t, hash)
		})

		t.Run("ReadAllString", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedData := []byte("test content")
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			str, err := testFile.ReadAllString(t.Context())
			require.NoError(t, err)
			assert.Equal(t, "test content", str)
		})

		t.Run("ReadAllStringContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			expectedData := []byte("test content")
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			str, err := testFile.ReadAllString(t.Context())
			require.NoError(t, err)
			assert.Equal(t, "test content", str)
		})

		t.Run("ReadJSON", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			jsonData := []byte(`{"name": "test", "value": 123}`)
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return jsonData, nil
			}

			var result map[string]any
			err := testFile.ReadJSON(t.Context(), &result)
			require.NoError(t, err)
			assert.Equal(t, "test", result["name"])
			assert.Equal(t, float64(123), result["value"])
		})

		t.Run("ReadXML", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			xmlData := []byte(`<root><name>test</name><value>123</value></root>`)
			testMockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return xmlData, nil
			}

			var result struct {
				Name  string `xml:"name"`
				Value int    `xml:"value"`
			}
			err := testFile.ReadXML(t.Context(), &result)
			require.NoError(t, err)
			assert.Equal(t, "test", result.Name)
			assert.Equal(t, 123, result.Value)
		})

		t.Run("Watch", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// fstest.MockFullyFeaturedFileSystem implements Watch
			cancel, err := testFile.Watch(func(f File, e Event) {})
			require.NoError(t, err)
			require.NotNil(t, cancel)

			// Test cancel function works
			err = cancel()
			require.NoError(t, err)
		})

		t.Run("Truncate", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// fstest.MockFullyFeaturedFileSystem implements Truncate
			err := testFile.Truncate(t.Context(), 100)
			require.NoError(t, err)
		})

		t.Run("Rename", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// fstest.MockFullyFeaturedFileSystem implements Rename
			renamedFile, err := testFile.Rename("newfile.txt")
			require.NoError(t, err)
			assert.True(t, strings.HasSuffix(string(renamedFile), "/test/path/to/newfile.txt"))
		})

		t.Run("Renamef", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")

			// fstest.MockFullyFeaturedFileSystem implements Rename
			renamedFile, err := testFile.Renamef("newfile_%d.txt", 123)
			require.NoError(t, err)
			assert.True(t, strings.HasSuffix(string(renamedFile), "/test/path/to/newfile_123.txt"))
		})

		t.Run("MoveTo", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")
			dest := File("mock" + t.Name() + "://test/path/to/destination.txt")

			// fstest.MockFullyFeaturedFileSystem implements Move,
			// the destination is a file (default MockStat) so it is the final path
			var movedTo string
			testMockFS.MockMove = func(filePath string, destPath string) error {
				assert.Equal(t, "/test/path/to/file.txt", filePath)
				movedTo = destPath
				return nil
			}
			err := testFile.MoveTo(t.Context(), dest)
			require.NoError(t, err)
			assert.Equal(t, "/test/path/to/destination.txt", movedTo)
		})

		t.Run("MoveTo_IntoDir", func(t *testing.T) {
			// An existing directory as destination means "move into":
			// MoveFileSystem.Move always receives the final path.
			testMockFS := createMockFS("mock" + t.Name() + "://")
			Register(testMockFS)
			t.Cleanup(func() { Unregister(testMockFS) })

			testFile := File("mock" + t.Name() + "://test/path/to/file.txt")
			destDir := File("mock" + t.Name() + "://test/other")

			testMockFS.MockStat = func(filePath string) (*FileInfo, error) {
				return &FileInfo{Name: path.Base(filePath), Exists: true, IsDir: filePath == "/test/other"}, nil
			}
			var movedTo string
			testMockFS.MockMove = func(filePath string, destPath string) error {
				movedTo = destPath
				return nil
			}
			err := testFile.MoveTo(t.Context(), destDir)
			require.NoError(t, err)
			assert.Equal(t, "/test/other/file.txt", movedTo)
		})

		t.Run("MoveTo_SamePath", func(t *testing.T) {
			// Package-level Move and File.MoveTo must short-circuit
			// same-path moves without delegating to FileSystem.Move OR
			// falling through to the copy+delete recursive fallback.
			// The latter would silently destroy the file.
			tmp := MustMakeTempDir()
			t.Cleanup(func() { _ = tmp.RemoveRecursive(context.Background()) })

			file := tmp.Join("a.txt")
			require.NoError(t, file.WriteAll(t.Context(), []byte("payload")))

			require.NoError(t, file.MoveTo(t.Context(), file), "File.MoveTo(t.Context(), self) must be a no-op")
			require.True(t, file.Exists(), "file survives same-path MoveTo")
			got, err := file.ReadAllString(t.Context())
			require.NoError(t, err)
			assert.Equal(t, "payload", got, "content preserved")

			// Same again via the package-level Move with explicit context.
			require.NoError(t, Move(t.Context(), file, file), "Move(ctx, src, src) must be a no-op")
			require.True(t, file.Exists())
		})

		t.Run("Remove", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			mockFS.MockRemove = func(filePath string) error {
				assert.Equal(t, "/test/path/to/file.txt", filePath)
				return nil
			}

			err := file.Remove()
			require.NoError(t, err)
		})

		t.Run("RemoveRecursive", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			// RemoveRecursive uses the native RemoveAll of
			// fstest.MockFullyFeaturedFileSystem instead of
			// listing and removing the entries one by one
			removed := ""
			mockFS.MockRemoveAll = func(ctx context.Context, filePath string) error {
				removed = filePath
				return nil
			}

			err := file.RemoveRecursive(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "/test/path/to/file.txt", removed)
		})

		t.Run("StdFS", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			stdFS := file.StdFS()
			assert.NotNil(t, stdFS)
			assert.Equal(t, file, stdFS.File)
		})

		t.Run("StdDirEntry", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Use a file with the prefix of this mock file system
			file := File("mock" + t.Name() + "://test/path/to/file.txt")

			stdDirEntry := file.StdDirEntry()
			assert.NotNil(t, stdDirEntry)
			assert.Equal(t, file, stdDirEntry.File)
		})

		t.Run("EmptyFile", func(t *testing.T) {
			emptyFile := File("")

			// Test various methods with empty file
			assert.Equal(t, "", emptyFile.RawURI())
			assert.Equal(t, "", emptyFile.Name())
			assert.Equal(t, InvalidFile, emptyFile.Dir())

			// Test methods that should return errors for empty files
			_, err := emptyFile.OpenReader()
			assert.Equal(t, ErrEmptyPath, err)

			_, err = emptyFile.OpenWriter()
			assert.Equal(t, ErrEmptyPath, err)

			err = emptyFile.Remove()
			assert.Equal(t, ErrEmptyPath, err)

			err = emptyFile.CheckExists()
			assert.Equal(t, ErrEmptyPath, err)
		})
	})
}

// TestFile_WriteAllContext_FallbackTruncates is a regression test for a bug
// where File.WriteAllContext fell back to OpenReadWriter (O_RDWR|O_CREATE,
// non-truncating) for file systems without a native WriteAll. Writing fewer
// bytes over an existing larger file left stale trailing bytes behind, e.g.
// writing a small JSON document over a larger one produced invalid JSON.
// The fallback must use OpenWriter (O_TRUNC) so the file is truncated first.
func TestFile_WriteAllContext_FallbackTruncates(t *testing.T) {
	// stored holds the current file content for a single mock file.
	var stored []byte

	// Base MockFileSystem deliberately does NOT implement WriteAllFileSystem
	// (nor ReadWriterFileSystem anymore), so File.WriteAllContext exercises
	// the OpenWriter fallback.
	mockFS := &fstest.MockFileSystem{
		MockPrefix: "mockwritealltrunc://",
		// OpenWriter mimics O_TRUNC: writing starts from an empty buffer.
		MockOpenWriter: func(filePath string, perm Permissions) (WriteCloser, error) {
			assert.Equal(t, "/test.json", filePath)
			return fsimpl.NewWriteOnCloseFileBuffer(nil, func(data []byte) error {
				stored = bytes.Clone(data)
				return nil
			}), nil
		},
	}
	Register(mockFS)
	t.Cleanup(func() { Unregister(mockFS) })

	file := File("mockwritealltrunc://test.json")

	// Seed the file with a large JSON document.
	stored = []byte(`{"key":"a large json document with plenty of content"}`)

	// Overwrite with a smaller JSON document.
	small := []byte(`{"k":1}`)
	err := file.WriteAll(t.Context(), small)
	require.NoError(t, err)

	// The fallback must truncate: no stale trailing bytes from the larger file.
	require.Equal(t, small, stored)
}
