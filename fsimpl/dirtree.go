package fsimpl

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DirTreeNode is a node of a directory tree built from the
// slash separated entry names of an archive, with the implicit
// directories along the entry paths.
type DirTreeNode struct {
	Path     string // slash separated path within the archive without leading slash
	Name     string
	IsDir    bool
	Size     int64
	Modified time.Time
	children map[string]*DirTreeNode // nil for files
}

// NewDirTree returns the root node of an empty directory tree.
func NewDirTree() *DirTreeNode {
	return &DirTreeNode{IsDir: true, children: make(map[string]*DirTreeNode)}
}

// Add adds the archive entry with the passed name to the tree,
// creating all directories along its path.
// Entry names ending with a slash are directories.
// An error is returned if the entry conflicts with an existing
// node of the other kind (file versus directory).
func (n *DirTreeNode) Add(entryName string, modified time.Time, size int64) error {
	entryName = strings.TrimPrefix(entryName, "/")
	isDir := strings.HasSuffix(entryName, "/")
	parts := strings.Split(strings.TrimSuffix(entryName, "/"), "/")
	current := n
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		last := i == len(parts)-1
		childIsDir := isDir || !last
		child, ok := current.children[part]
		if !ok {
			child = &DirTreeNode{
				Path:     strings.Join(parts[:i+1], "/"),
				Name:     part,
				IsDir:    childIsDir,
				Modified: modified,
			}
			if childIsDir {
				child.children = make(map[string]*DirTreeNode)
			} else {
				child.Size = size
			}
			current.children[part] = child
		} else if child.IsDir != childIsDir {
			return fmt.Errorf("archive entry %q conflicts with existing %s %q", entryName, kindOf(child.IsDir), child.Path)
		}
		current = child
	}
	return nil
}

func kindOf(isDir bool) string {
	if isDir {
		return "directory"
	}
	return "file"
}

// Lookup returns the node at the slash separated path or nil.
func (n *DirTreeNode) Lookup(path string) *DirTreeNode {
	current := n
	for part := range strings.SplitSeq(strings.Trim(path, "/"), "/") {
		if part == "" || part == "." {
			continue
		}
		child, ok := current.children[part]
		if !ok {
			return nil
		}
		current = child
	}
	return current
}

// SortedChildren returns the children sorted with directories first.
func (n *DirTreeNode) SortedChildren() []*DirTreeNode {
	s := make([]*DirTreeNode, 0, len(n.children))
	for _, child := range n.children {
		s = append(s, child)
	}
	sort.Slice(s, func(i, j int) bool {
		if s[i].IsDir != s[j].IsDir {
			return s[i].IsDir
		}
		return s[i].Name < s[j].Name
	})
	return s
}
