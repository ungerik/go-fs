//go:build linux || freebsd || netbsd || darwin || solaris

package fs

import "github.com/pkg/xattr"

// xattrCreate and xattrReplace mirror the platform-specific
// [xattr.XATTR_CREATE] and [xattr.XATTR_REPLACE] flag values so that
// [MemFileSystem] interprets SetXAttr flags the same way the local file
// system does. The pkg/xattr package only defines these constants on
// platforms with native xattr support; see xattrflags_unsupported.go for
// the fallback used on platforms without it (e.g. Windows).
const (
	xattrCreate  = xattr.XATTR_CREATE
	xattrReplace = xattr.XATTR_REPLACE
)
