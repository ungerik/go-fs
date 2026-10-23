//go:build linux || darwin || solaris

package fs

import "github.com/pkg/xattr"

// xattrNativeFlags returns the platform values of [xattr.XATTR_CREATE]
// and [xattr.XATTR_REPLACE] for flags. The pkg/xattr package only defines
// these constants on some platforms, see xattrflags_other.go
// for the platforms without them.
func xattrNativeFlags(flags XAttrFlags) (native int, ok bool) {
	if flags&XAttrCreate != 0 {
		native |= xattr.XATTR_CREATE
	}
	if flags&XAttrReplace != 0 {
		native |= xattr.XATTR_REPLACE
	}
	return native, true
}
