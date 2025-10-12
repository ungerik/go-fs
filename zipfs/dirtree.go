package zipfs

import (
	"path"
	"sort"
	"time"

	"github.com/ungerik/go-fs"
)

// dirTreeNode represents a single file or directory in the ZIP archive's directory tree.
// It forms a tree structure used to navigate and list the contents of a ZIP file
// efficiently without requiring linear scans through all entries.
//
// Structure:
//   - Each dirTreeNode embeds a fs.FileInfo containing metadata (name, size, permissions, etc.)
//   - Directory nodes have a children map pointing to their child nodes
//   - File nodes have nil children (no children map allocated)
//
// Usage:
//   - Built dynamically when listing directory contents (see ListDirInfoRecursive)
//   - The root dirTreeNode represents the ZIP archive root with IsDir=true
//   - Navigation through the tree is done by following the children map
//   - Enables efficient hierarchical listing and path resolution
//
// Example tree structure for a ZIP containing:
//   - dir1/file1.txt
//   - dir1/file2.txt
//   - dir2/file3.txt
//
// Would be represented as:
//
//	root (dirTreeNode)
//	├── dir1 (dirTreeNode with children map)
//	│   ├── file1.txt (dirTreeNode, no children)
//	│   └── file2.txt (dirTreeNode, no children)
//	└── dir2 (dirTreeNode with children map)
//	    └── file3.txt (dirTreeNode, no children)
type dirTreeNode struct {
	*fs.FileInfo
	// children maps child names to their corresponding nodes.
	// Only allocated for directory nodes (IsDir=true).
	// For file nodes, this field is nil.
	children map[string]*dirTreeNode
}

// func (n *node) hasChild(name string) bool {
// 	return n.children != nil && n.children[name] != nil
// }

// sortedChildren returns all child nodes sorted in a consistent order.
// Directories are listed before files, and within each group (directories/files),
// entries are sorted alphabetically by name.
//
// This sorting ensures consistent directory listings across different platforms
// and makes directory traversal predictable for users.
//
// Returns nil if the node has no children or is a file node.
func (n *dirTreeNode) sortedChildren() []*dirTreeNode {
	l := len(n.children)
	if l == 0 {
		return nil
	}
	s := make([]*dirTreeNode, l)
	i := 0
	for _, n := range n.children {
		s[i] = n
		i++
	}
	sort.Slice(s, func(i, j int) bool {
		if s[i].IsDir != s[j].IsDir {
			return s[i].IsDir
		}
		return s[i].Name < s[j].Name
	})
	return s
}

// func (n *node) sortedChildDirs() []*node {
// 	l := len(n.children)
// 	if l == 0 {
// 		return nil
// 	}
// 	s := make([]*node, 0, l)
// 	for _, n := range n.children {
// 		if n.IsDir {
// 			s = append(s, n)
// 		}
// 	}
// 	sort.Slice(s, func(i, j int) bool {
// 		return s[i].Name < s[j].Name
// 	})
// 	return s
// }

// func (n *node) addChild(info fs.FileInfo) {
// 	child := &node{FileInfo: info}
// 	if info.IsDir {
// 		child.children = make(map[string]*node)
// 	}
// 	n.children[info.Name] = child
// }

// addChildDir adds or returns an existing child directory node.
// If a directory with the given name already exists as a child, it is returned.
// Otherwise, a new directory node is created with the specified path and modification time.
//
// The directory node is created with:
//   - IsDir=true, IsRegular=true
//   - AllRead permissions (read-only)
//   - Size=0 (directories have no size)
//   - IsHidden=true if the name starts with '.'
//   - An initialized children map for storing subdirectories and files
//
// Panics if a child with the same name exists but is not a directory.
func (n *dirTreeNode) addChildDir(filePath string, modTime time.Time) (child *dirTreeNode) {
	name := path.Base(filePath)
	child = n.children[name]
	if child != nil {
		if !child.IsDir {
			panic("existing child is not a directory")
		}
		return child
	}
	child = &dirTreeNode{
		FileInfo: &fs.FileInfo{
			File:        fs.File(filePath),
			Name:        name,
			Exists:      true,
			IsDir:       true,
			IsRegular:   true,
			IsHidden:    name[0] == '.',
			Size:        0,
			Modified:    modTime,
			Permissions: fs.AllRead,
		},
		children: make(map[string]*dirTreeNode),
	}
	n.children[name] = child
	return child
}

// addChildFile adds or returns an existing child file node.
// If a file with the given name already exists as a child, it is returned.
// Otherwise, a new file node is created with the specified path, modification time, and size.
//
// The file node is created with:
//   - IsDir=false, IsRegular=true
//   - AllRead permissions (read-only)
//   - The specified size (uncompressed size from ZIP)
//   - IsHidden=true if the name starts with '.'
//   - No children map (files don't have children)
//
// Panics if a child with the same name exists but is a directory.
func (n *dirTreeNode) addChildFile(filePath string, modTime time.Time, size int64) (child *dirTreeNode) {
	name := path.Base(filePath)
	child = n.children[name]
	if child != nil {
		if child.IsDir {
			panic("existing child is a directory")
		}
		return child
	}
	child = &dirTreeNode{
		FileInfo: &fs.FileInfo{
			File:        fs.File(filePath),
			Name:        name,
			Exists:      true,
			IsDir:       false,
			IsRegular:   true,
			IsHidden:    name[0] == '.',
			Size:        size,
			Modified:    modTime,
			Permissions: fs.AllRead,
		},
	}
	n.children[name] = child
	return child
}
