package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ungerik/go-fs/fsimpl"
)

// mockFileInfo implements io/fs.FileInfo for testing
type mockFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (m *mockFileInfo) Name() string       { return m.name }
func (m *mockFileInfo) Size() int64        { return m.size }
func (m *mockFileInfo) Mode() os.FileMode  { return m.mode }
func (m *mockFileInfo) ModTime() time.Time { return m.modTime }
func (m *mockFileInfo) IsDir() bool        { return m.isDir }
func (m *mockFileInfo) Sys() interface{}   { return nil }

// mockReadCloser implements iofs.File for testing
type mockReadCloser struct {
	io.ReadCloser
}

func (m *mockReadCloser) Stat() (iofs.FileInfo, error) {
	return &mockFileInfo{name: "test.txt", size: 12}, nil
}

func (m *mockReadCloser) ReadDir(n int) ([]iofs.DirEntry, error) {
	return nil, errors.New("not a directory")
}

// mockWriteCloser implements WriteCloser for testing
type mockWriteCloser struct {
	io.Writer
}

func (m *mockWriteCloser) Close() error {
	return nil
}

func (m *mockWriteCloser) Write(p []byte) (n int, err error) {
	return len(p), nil
}

// TestFile comprehensively tests File methods using MockFileSystem with different Permissions
func TestFile(t *testing.T) {
	// Helper to create a mock file system with minimal setup
	createMockFS := func(prefix string) *MockFullyFeaturedFileSystem {
		return &MockFullyFeaturedFileSystem{
			MockFileSystem: MockFileSystem{
				MockPrefix: prefix,
				MockCleanPathFromURI: func(uri string) string {
					// Simple implementation that removes the prefix and returns the path
					if strings.HasPrefix(uri, prefix) {
						path := strings.TrimPrefix(uri, prefix)
						// Ensure path starts with /
						if !strings.HasPrefix(path, "/") {
							path = "/" + path
						}
						return path
					}
					return uri
				},
				MockSplitDirAndName: func(path string) (string, string) {
					// Simple implementation that splits on the last /
					lastSlash := strings.LastIndex(path, "/")
					if lastSlash == -1 {
						return "", path
					}
					return path[:lastSlash], path[lastSlash+1:]
				},
				MockJoinCleanFile: func(elements ...string) File {
					// Simple implementation that joins elements with /
					path := strings.Join(elements, "/")
					// Clean up double slashes
					path = strings.ReplaceAll(path, "//", "/")
					return File(prefix + path)
				},
				MockJoinCleanPath: func(elements ...string) string {
					// Simple implementation that joins elements with /
					path := strings.Join(elements, "/")
					// Clean up double slashes
					path = strings.ReplaceAll(path, "//", "/")
					return path
				},
				MockStat: func(path string) (iofs.FileInfo, error) {
					// Default implementation that returns a file info
					return &mockFileInfo{
						name:  "file.txt",
						isDir: false,
						size:  100,
						mode:  0644,
					}, nil
				},
				MockMakeDir: func(dirPath string, perm []Permissions) error {
					return nil
				},
				MockRemove: func(filePath string) error {
					return nil
				},
			},
			MockOpenAppendWriter: func(filePath string, perm []Permissions) (WriteCloser, error) {
				// Default implementation that returns a mock writer
				return &mockWriteCloser{}, nil
			},
			MockReadAll: func(ctx context.Context, filePath string) ([]byte, error) {
				// Default implementation that returns empty data
				return []byte{}, nil
			},
			MockWriteAll: func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
				// Default implementation that does nothing
				return nil
			},
			MockListDirMax: func(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
				// Default implementation that returns empty list
				return []File{}, nil
			},
		}
	}

	// Test different permission combinations for each method
	permissionTests := []struct {
		name        string
		permissions []Permissions
	}{
		{"NoPermissions", []Permissions{}},
		{"SinglePermission", []Permissions{0644}},
		{"MultiplePermissions", []Permissions{0644, 0755}},
		{"ReadOnly", []Permissions{0444}},
		{"WriteOnly", []Permissions{0222}},
		{"ExecuteOnly", []Permissions{0111}},
		{"FullPermissions", []Permissions{0777}},
		{"UserReadWrite", []Permissions{0600}},
		{"GroupReadWrite", []Permissions{0660}},
		{"OtherReadWrite", []Permissions{0606}},
		{"StickyBit", []Permissions{01777}},
		{"SetUID", []Permissions{04755}},
		{"SetGID", []Permissions{02755}},
	}

	t.Run("MakeDir", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				var capturedPerms []Permissions
				mockFS.MockMakeDir = func(dirPath string, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", dirPath)
					return nil
				}

				err := file.MakeDir(permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
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

				// Mock Stat to return file not found
				mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
					return nil, errors.New("file not found")
				}

				var capturedPerms []Permissions
				mockFS.MockMakeDir = func(dirPath string, perm []Permissions) error {
					capturedPerms = perm
					return nil
				}

				err := file.MakeAllDirs(permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
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

				var capturedPerms []Permissions
				mockFS.MockOpenWriter = func(filePath string, perm []Permissions) (WriteCloser, error) {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					return &fsimpl.FileBuffer{}, nil
				}

				writer, err := file.OpenWriter(permTest.permissions...)
				require.NoError(t, err)
				require.NotNil(t, writer)
				require.Equal(t, permTest.permissions, capturedPerms)
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

				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					return nil
				}

				writer, err := file.OpenAppendWriter(permTest.permissions...)
				require.NoError(t, err)
				require.NotNil(t, writer)

				// Close to trigger WriteAll
				err = writer.Close()
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
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

				var capturedPerms []Permissions
				mockFS.MockOpenReadWriter = func(filePath string, perm []Permissions) (ReadWriteSeekCloser, error) {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					return &fsimpl.FileBuffer{}, nil
				}

				readWriter, err := file.OpenReadWriter(permTest.permissions...)
				require.NoError(t, err)
				require.NotNil(t, readWriter)
				require.Equal(t, permTest.permissions, capturedPerms)
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
				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					require.Equal(t, testData, data)
					return nil
				}

				err := file.WriteAll(testData, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
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
				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					require.Equal(t, testData, data)
					return nil
				}

				err := file.WriteAllContext(context.Background(), testData, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
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
				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					assert.Equal(t, []byte(testStr), data)
					return nil
				}

				err := file.WriteAllString(testStr, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
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
				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					assert.Equal(t, []byte(testStr), data)
					return nil
				}

				err := file.WriteAllStringContext(context.Background(), testStr, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
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
				var capturedPerms []Permissions
				mockFS.MockAppend = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					require.Equal(t, testData, data)
					return nil
				}

				err := file.Append(context.Background(), testData, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
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
				var capturedPerms []Permissions
				mockFS.MockAppend = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					assert.Equal(t, []byte(testStr), data)
					return nil
				}

				err := file.AppendString(context.Background(), testStr, permTest.permissions...)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("WriteJSON", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testData := map[string]interface{}{"name": "test", "value": 123}
				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					// Verify it's valid JSON
					assert.Contains(t, string(data), `"name":"test"`)
					assert.Contains(t, string(data), `"value":123`)
					return nil
				}

				err := file.WriteJSON(context.Background(), testData)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
	})

	t.Run("WriteXML", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				testData := struct {
					Name  string `xml:"name"`
					Value int    `xml:"value"`
				}{Name: "test", Value: 123}

				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					// Verify it's valid XML with header
					assert.Contains(t, string(data), `<?xml version="1.0" encoding="UTF-8"?>`)
					assert.Contains(t, string(data), `<name>test</name>`)
					assert.Contains(t, string(data), `<value>123</value>`)
					return nil
				}

				err := file.WriteXML(context.Background(), testData)
				require.NoError(t, err)
				require.Equal(t, permTest.permissions, capturedPerms)
			})
		}
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
				mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
					return &mockFileInfo{
						name: "file.txt",
						size: 0,
						mode: 0644,
					}, nil
				}

				var capturedPerms []Permissions
				mockFS.MockOpenWriter = func(filePath string, perm []Permissions) (WriteCloser, error) {
					capturedPerms = perm
					assert.Equal(t, "/test/path/to/file.txt", filePath)
					return &fsimpl.FileBuffer{}, nil
				}

				n, err := file.ReadFrom(testReader)
				require.NoError(t, err)
				assert.Equal(t, int64(12), n) // "test content" length
				// ReadFrom should use existing file permissions, not the test permissions
				assert.Equal(t, []Permissions{0644}, capturedPerms)
			})
		}
	})

	t.Run("GobDecode", func(t *testing.T) {
		for _, permTest := range permissionTests {
			t.Run(permTest.name, func(t *testing.T) {
				// Create mock file system for this test with only needed functions
				mockFS := createMockFS("mock" + t.Name() + "://")
				Register(mockFS)
				t.Cleanup(func() { Unregister(mockFS) })

				file := File("mock" + t.Name() + "://test/path/to/file.txt")

				// First encode some data
				mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
					return []byte("test content"), nil
				}

				encodedData, err := file.GobEncode()
				require.NoError(t, err)

				// Now decode it - GobDecode doesn't take permissions, but WriteAll does
				var capturedPerms []Permissions
				mockFS.MockWriteAll = func(ctx context.Context, filePath string, data []byte, perm []Permissions) error {
					capturedPerms = perm
					assert.Equal(t, []byte("test content"), data)
					return nil
				}

				err = file.GobDecode(encodedData)
				require.NoError(t, err)
				// GobDecode uses default permissions (empty slice)
				assert.Equal(t, []Permissions{}, capturedPerms)
			})
		}
	})

	// Test methods that don't take permissions but should still work
	t.Run("NonPermissionMethods", func(t *testing.T) {
		file := File("mock" + t.Name() + "://test/path/to/file.txt")

		t.Run("FileSystem", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			fs := file.FileSystem()
			require.Equal(t, mockFS, fs)
		})

		t.Run("ParseRawURI", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			fs, path := file.ParseRawURI()
			require.Equal(t, mockFS, fs)
			assert.Equal(t, "/test/path/to/file.txt", path)
		})

		t.Run("RawURI", func(t *testing.T) {
			uri := file.RawURI()
			assert.Equal(t, "mock://test/path/to/file.txt", uri)
		})

		t.Run("String", func(t *testing.T) {
			str := file.String()
			assert.Equal(t, "mock://test/path/to/file.txt", str)
		})

		t.Run("URL", func(t *testing.T) {
			url := file.URL()
			assert.Equal(t, "mock://test/path/to/file.txt", url)
		})

		t.Run("Path", func(t *testing.T) {
			path := file.Path()
			assert.Equal(t, "/path/to/file.txt", path)
		})

		t.Run("PathWithSlashes", func(t *testing.T) {
			path := file.PathWithSlashes()
			assert.Equal(t, "/path/to/file.txt", path)
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
			assert.Equal(t, File("mock://test/path/to"), dir)
		})

		t.Run("DirAndName", func(t *testing.T) {
			dir, name := file.DirAndName()
			assert.Equal(t, File("mock://test/path/to"), dir)
			assert.Equal(t, "file.txt", name)
		})

		t.Run("VolumeName", func(t *testing.T) {
			volume := file.VolumeName()
			assert.Equal(t, "", volume) // MockFileSystem doesn't implement VolumeNameFileSystem
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
			assert.Equal(t, File("mock://test/path/to/file"), trimmed)
		})

		t.Run("Join", func(t *testing.T) {
			joined := file.Join("subdir", "nested.txt")
			assert.Equal(t, File("mock://test/path/to/file.txt/subdir/nested.txt"), joined)
		})

		t.Run("Joinf", func(t *testing.T) {
			joined := file.Joinf("file_%d.txt", 123)
			assert.Equal(t, File("mock://test/path/to/file.txt/file_123.txt"), joined)
		})

		t.Run("IsReadable", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock Stat to return a readable file
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: false,
					size:  100,
					mode:  0644,
				}, nil
			}

			readable := file.IsReadable()
			require.True(t, readable)
		})

		t.Run("IsWriteable", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock Stat to return a writable file
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: false,
					size:  100,
					mode:  0644,
				}, nil
			}

			writeable := file.IsWriteable()
			require.True(t, writeable)
		})

		t.Run("Stat", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedInfo := &mockFileInfo{
				name:  "file.txt",
				isDir: false,
				size:  100,
				mode:  0644,
			}

			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return expectedInfo, nil
			}

			info, err := file.Stat()
			require.NoError(t, err)
			require.Equal(t, expectedInfo, info)
		})

		t.Run("Info", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedInfo := &mockFileInfo{
				name:  "file.txt",
				isDir: false,
				size:  100,
				mode:  0644,
			}

			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return expectedInfo, nil
			}

			info := file.Info()
			require.NotNil(t, info)
			assert.Equal(t, file, info.File)
		})

		t.Run("Exists", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock Stat to return no error (file exists)
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{name: "file.txt"}, nil
			}

			exists := file.Exists()
			require.True(t, exists)
		})

		t.Run("CheckExists", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Test with existing file
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{name: "file.txt"}, nil
			}

			err := file.CheckExists()
			require.NoError(t, err)

			// Test with non-existing file
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return nil, errors.New("file not found")
			}

			err = file.CheckExists()
			require.Error(t, err)
			require.IsType(t, &ErrDoesNotExist{}, err)
		})

		t.Run("IsDir", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock Stat to return a directory
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: true,
				}, nil
			}

			isDir := file.IsDir()
			require.True(t, isDir)
		})

		t.Run("CheckIsDir", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Test with directory
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: true,
				}, nil
			}

			err := file.CheckIsDir()
			require.NoError(t, err)

			// Test with file (not directory)
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: false,
				}, nil
			}

			err = file.CheckIsDir()
			require.Error(t, err)
			require.IsType(t, &ErrIsNotDirectory{}, err)
		})

		t.Run("AbsPath", func(t *testing.T) {
			absPath := file.AbsPath()
			assert.Equal(t, "/path/to/file.txt", absPath)
		})

		t.Run("HasAbsPath", func(t *testing.T) {
			hasAbs := file.HasAbsPath()
			assert.False(t, hasAbs) // path doesn't start with /
		})

		t.Run("ToAbsPath", func(t *testing.T) {
			absFile := file.ToAbsPath()
			assert.Equal(t, File("/path/to/file.txt"), absFile)
		})

		t.Run("IsRegular", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock Stat to return a regular file
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: false,
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

			// Mock ListDirMax to return empty list
			mockFS.MockListDirMax = func(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
				return []File{}, nil
			}

			isEmpty := file.IsEmptyDir()
			assert.True(t, isEmpty)
		})

		t.Run("IsHidden", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			hiddenFile := File("mock" + t.Name() + "://test/path/to/.hidden")
			hidden := hiddenFile.IsHidden()
			assert.True(t, hidden)
		})

		t.Run("IsSymbolicLink", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			symlink := file.IsSymbolicLink()
			assert.False(t, symlink) // MockFileSystem returns false
		})

		t.Run("Size", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock Stat to return a file with size
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name: "file.txt",
					size: 1024,
				}, nil
			}

			size := file.Size()
			assert.Equal(t, int64(1024), size)
		})

		t.Run("ContentHash", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock OpenReader to return a reader with content
			mockFS.MockOpenReader = func(path string) (ReadCloser, error) {
				return &mockReadCloser{io.NopCloser(strings.NewReader("test content"))}, nil
			}

			hash, err := file.ContentHash()
			require.NoError(t, err)
			assert.NotEmpty(t, hash)
		})

		t.Run("ContentHashContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// Mock OpenReader to return a reader with content
			mockFS.MockOpenReader = func(path string) (ReadCloser, error) {
				return &mockReadCloser{io.NopCloser(strings.NewReader("test content"))}, nil
			}

			hash, err := file.ContentHashContext(context.Background())
			require.NoError(t, err)
			assert.NotEmpty(t, hash)
		})

		t.Run("Modified", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedTime := time.Now()
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:    "file.txt",
					modTime: expectedTime,
				}, nil
			}

			modified := file.Modified()
			assert.Equal(t, expectedTime, modified)
		})

		t.Run("Permissions", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name: "file.txt",
					mode: 0644,
				}, nil
			}

			perm := file.Permissions()
			assert.Equal(t, Permissions(0644), perm)
		})

		t.Run("SetPermissions", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFileSystem doesn't implement PermissionsFileSystem
			err := file.SetPermissions(0644)
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
		})

		t.Run("ListDir", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			mockFS.MockListDirInfo = func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
				for _, f := range expectedFiles {
					info := &FileInfo{File: f, Name: f.Name(), IsDir: false}
					if err := callback(info); err != nil {
						return err
					}
				}
				return nil
			}

			var listedFiles []File
			err := file.ListDir(func(f File) error {
				listedFiles = append(listedFiles, f)
				return nil
			})

			require.NoError(t, err)
			assert.Equal(t, expectedFiles, listedFiles)
		})

		t.Run("ListDirContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			mockFS.MockListDirInfo = func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
				for _, f := range expectedFiles {
					info := &FileInfo{File: f, Name: f.Name(), IsDir: false}
					if err := callback(info); err != nil {
						return err
					}
				}
				return nil
			}

			var listedFiles []File
			err := file.ListDirContext(context.Background(), func(f File) error {
				listedFiles = append(listedFiles, f)
				return nil
			})

			require.NoError(t, err)
			assert.Equal(t, expectedFiles, listedFiles)
		})

		t.Run("ListDirIter", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			mockFS.MockListDirInfo = func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
				for _, f := range expectedFiles {
					info := &FileInfo{File: f, Name: f.Name(), IsDir: false}
					if err := callback(info); err != nil {
						return err
					}
				}
				return nil
			}

			var listedFiles []File
			for f, err := range file.ListDirIter() {
				require.NoError(t, err)
				listedFiles = append(listedFiles, f)
			}

			assert.Equal(t, expectedFiles, listedFiles)
		})

		t.Run("ListDirMax", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedFiles := []File{
				File("mock" + t.Name() + "://test/path/to/dir/file1.txt"),
				File("mock" + t.Name() + "://test/path/to/dir/file2.txt"),
			}

			mockFS.MockListDirMax = func(ctx context.Context, dirPath string, max int, patterns []string) ([]File, error) {
				return expectedFiles, nil
			}

			files, err := file.ListDirMax(10)
			require.NoError(t, err)
			assert.Equal(t, expectedFiles, files)
		})

		t.Run("User", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFileSystem doesn't implement UserFileSystem
			user, err := file.User()
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
			assert.Empty(t, user)
		})

		t.Run("SetUser", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFileSystem doesn't implement UserFileSystem
			err := file.SetUser("testuser")
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
		})

		t.Run("Group", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFileSystem doesn't implement GroupFileSystem
			group, err := file.Group()
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
			assert.Empty(t, group)
		})

		t.Run("SetGroup", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFileSystem doesn't implement GroupFileSystem
			err := file.SetGroup("testgroup")
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
		})

		t.Run("Touch", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFileSystem doesn't implement TouchFileSystem
			err := file.Touch()
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
		})

		t.Run("WriteTo", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			mockFS.MockOpenReader = func(path string) (ReadCloser, error) {
				return &mockReadCloser{io.NopCloser(strings.NewReader("test content"))}, nil
			}

			var buf bytes.Buffer
			n, err := file.WriteTo(&buf)
			require.NoError(t, err)
			assert.Equal(t, int64(12), n) // "test content" length
			assert.Equal(t, "test content", buf.String())
		})

		t.Run("OpenReader", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedReader := &mockReadCloser{io.NopCloser(strings.NewReader("test content"))}
			mockFS.MockOpenReader = func(path string) (ReadCloser, error) {
				return expectedReader, nil
			}

			reader, err := file.OpenReader()
			require.NoError(t, err)
			assert.Equal(t, expectedReader, reader)
		})

		t.Run("OpenReadSeeker", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedReader := &mockReadCloser{io.NopCloser(strings.NewReader("test content"))}
			mockFS.MockOpenReader = func(path string) (ReadCloser, error) {
				return expectedReader, nil
			}

			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name: "file.txt",
					size: 12,
				}, nil
			}

			reader, err := file.OpenReadSeeker()
			require.NoError(t, err)
			require.NotNil(t, reader)
		})

		t.Run("ReadAll", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedData := []byte("test content")
			mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			data, err := file.ReadAll()
			require.NoError(t, err)
			assert.Equal(t, expectedData, data)
		})

		t.Run("ReadAllContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedData := []byte("test content")
			mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			data, err := file.ReadAllContext(context.Background())
			require.NoError(t, err)
			assert.Equal(t, expectedData, data)
		})

		t.Run("ReadAllContentHash", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedData := []byte("test content")
			mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			data, hash, err := file.ReadAllContentHash(context.Background())
			require.NoError(t, err)
			assert.Equal(t, expectedData, data)
			assert.NotEmpty(t, hash)
		})

		t.Run("ReadAllString", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedData := []byte("test content")
			mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			str, err := file.ReadAllString()
			require.NoError(t, err)
			assert.Equal(t, "test content", str)
		})

		t.Run("ReadAllStringContext", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			expectedData := []byte("test content")
			mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return expectedData, nil
			}

			str, err := file.ReadAllStringContext(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "test content", str)
		})

		t.Run("ReadJSON", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			jsonData := []byte(`{"name": "test", "value": 123}`)
			mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return jsonData, nil
			}

			var result map[string]interface{}
			err := file.ReadJSON(context.Background(), &result)
			require.NoError(t, err)
			assert.Equal(t, "test", result["name"])
			assert.Equal(t, float64(123), result["value"])
		})

		t.Run("ReadXML", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			xmlData := []byte(`<root><name>test</name><value>123</value></root>`)
			mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return xmlData, nil
			}

			var result struct {
				Name  string `xml:"name"`
				Value int    `xml:"value"`
			}
			err := file.ReadXML(context.Background(), &result)
			require.NoError(t, err)
			assert.Equal(t, "test", result.Name)
			assert.Equal(t, 123, result.Value)
		})

		t.Run("GobEncode", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			mockFS.MockReadAll = func(ctx context.Context, filePath string) ([]byte, error) {
				return []byte("test content"), nil
			}

			data, err := file.GobEncode()
			require.NoError(t, err)
			assert.NotEmpty(t, data)
		})

		t.Run("Watch", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFileSystem doesn't implement WatchFileSystem
			cancel, err := file.Watch(func(f File, e Event) {})
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
			assert.Nil(t, cancel)
		})

		t.Run("Truncate", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFileSystem doesn't implement TruncateFileSystem
			err := file.Truncate(100)
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
		})

		t.Run("Rename", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFileSystem doesn't implement RenameFileSystem or MoveFileSystem
			renamedFile, err := file.Rename("newfile.txt")
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
			assert.Equal(t, InvalidFile, renamedFile)
		})

		t.Run("Renamef", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			// MockFileSystem doesn't implement RenameFileSystem or MoveFileSystem
			renamedFile, err := file.Renamef("newfile_%d.txt", 123)
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
			assert.Equal(t, InvalidFile, renamedFile)
		})

		t.Run("MoveTo", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			dest := File("mock" + t.Name() + "://test/path/to/destination.txt")

			// MockFileSystem doesn't implement MoveFileSystem
			err := file.MoveTo(dest)
			require.Error(t, err)
			assert.IsType(t, &ErrUnsupported{}, err)
		})

		t.Run("Remove", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

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

			// Mock IsDir to return true
			mockFS.MockStat = func(path string) (iofs.FileInfo, error) {
				return &mockFileInfo{
					name:  "file.txt",
					isDir: true,
				}, nil
			}

			// Mock ListDir to return empty list
			mockFS.MockListDirInfo = func(ctx context.Context, dirPath string, callback func(*FileInfo) error, patterns []string) error {
				return nil
			}

			// Mock Remove
			mockFS.MockRemove = func(filePath string) error {
				return nil
			}

			err := file.RemoveRecursive()
			require.NoError(t, err)
		})

		t.Run("StdFS", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

			stdFS := file.StdFS()
			assert.NotNil(t, stdFS)
			assert.Equal(t, file, stdFS.File)
		})

		t.Run("StdDirEntry", func(t *testing.T) {
			// Create mock file system for this test with only needed functions
			mockFS := createMockFS("mock" + t.Name() + "://")
			Register(mockFS)
			t.Cleanup(func() { Unregister(mockFS) })

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
