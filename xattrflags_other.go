//go:build !linux && !darwin && !solaris

package fs

// xattrNativeFlags returns false because the pkg/xattr package
// defines no XATTR_CREATE and XATTR_REPLACE values on this platform,
// so the local file system emulates the flags.
func xattrNativeFlags(flags XAttrFlags) (native int, ok bool) {
	return 0, false
}
