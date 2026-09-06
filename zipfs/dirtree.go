package zipfs

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// dirTreeNode is a node of the directory tree
// built from the entry names of a ZIP archive.
type dirTreeNode struct {
	path     string // slash separated path within the archive without leading slash
	name     string
	isDir    bool
	size     int64
	modified time.Time
	children map[string]*dirTreeNode // nil for files
}

func newDirTreeRoot() *dirTreeNode {
	return &dirTreeNode{isDir: true, children: make(map[string]*dirTreeNode)}
}

// add adds the ZIP entry with the passed name to the tree,
// creating all directories along its path.
// Entry names ending with a slash are directories.
// An error is returned if the entry conflicts with an existing
// node of the other kind (file versus directory).
func (n *dirTreeNode) add(entryName string, modified time.Time, size int64) error {
	entryName = strings.TrimPrefix(entryName, Separator)
	isDir := strings.HasSuffix(entryName, Separator)
	parts := strings.Split(strings.TrimSuffix(entryName, Separator), Separator)
	current := n
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		last := i == len(parts)-1
		childIsDir := isDir || !last
		child, ok := current.children[part]
		if !ok {
			child = &dirTreeNode{
				path:     strings.Join(parts[:i+1], Separator),
				name:     part,
				isDir:    childIsDir,
				modified: modified,
			}
			if childIsDir {
				child.children = make(map[string]*dirTreeNode)
			} else {
				child.size = size
			}
			current.children[part] = child
		} else if child.isDir != childIsDir {
			return fmt.Errorf("ZIP entry %q conflicts with existing %s %q", entryName, kindOf(child.isDir), child.path)
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

// lookup returns the node at the slash separated dirPath or nil.
func (n *dirTreeNode) lookup(dirPath string) *dirTreeNode {
	current := n
	for part := range strings.SplitSeq(strings.Trim(dirPath, Separator), Separator) {
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

// sortedChildren returns the children sorted with directories first.
func (n *dirTreeNode) sortedChildren() []*dirTreeNode {
	s := make([]*dirTreeNode, 0, len(n.children))
	for _, child := range n.children {
		s = append(s, child)
	}
	sort.Slice(s, func(i, j int) bool {
		if s[i].isDir != s[j].isDir {
			return s[i].isDir
		}
		return s[i].name < s[j].name
	})
	return s
}
