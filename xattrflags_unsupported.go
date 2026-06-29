//go:build !linux && !freebsd && !netbsd && !darwin && !solaris

package fs

// xattrCreate and xattrReplace provide fallback values for platforms where
// the pkg/xattr package has no native xattr support and therefore does not
// define [xattr.XATTR_CREATE] / [xattr.XATTR_REPLACE] (e.g. Windows). The
// values match the common Linux/Solaris constants and only affect
// [MemFileSystem], the in-memory file system that implements xattrs on every
// platform. The local file system has no xattr support on these platforms.
const (
	xattrCreate  = 0x1
	xattrReplace = 0x2
)
