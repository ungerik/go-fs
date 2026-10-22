package fs

import (
	iofs "io/fs"
	"os"
)

// Permission bits following the Unix/os.FileMode schema.
// The zero value NoPermissions means "unspecified" everywhere
// in this package: a FileSystem substitutes its own default
// permissions for it, see [Permissions.OrDefault].
const (
	// NoPermissions grants nothing. As an argument it means
	// "use the file system default", see [Permissions.OrDefault].
	NoPermissions Permissions = 0

	// UserExecute is the owner's execute bit (0100).
	UserExecute Permissions = 0100
	// UserWrite is the owner's write bit (0200).
	UserWrite Permissions = 0200
	// UserRead is the owner's read bit (0400).
	UserRead Permissions = 0400
	// UserReadWrite lets the owner read and write (0600).
	UserReadWrite Permissions = UserRead | UserWrite
	// UserReadWriteExecute lets the owner read, write and execute (0700).
	UserReadWriteExecute Permissions = UserRead | UserWrite | UserExecute

	// GroupExecute is the group's execute bit (0010).
	GroupExecute Permissions = 0010
	// GroupWrite is the group's write bit (0020).
	GroupWrite Permissions = 0020
	// GroupRead is the group's read bit (0040).
	GroupRead Permissions = 0040
	// GroupReadWrite lets the group read and write (0060).
	GroupReadWrite Permissions = GroupRead | GroupWrite
	// GroupReadWriteExecute lets the group read, write and execute (0070).
	GroupReadWriteExecute Permissions = GroupRead | GroupWrite | GroupExecute

	// UserAndGroupRead lets the owner and the group read (0440).
	UserAndGroupRead Permissions = UserRead | GroupRead
	// UserAndGroupReadWrite lets the owner and the group read and write (0660).
	UserAndGroupReadWrite Permissions = UserReadWrite | GroupReadWrite
	// UserAndGroupReadWriteExecute lets the owner and the group
	// read, write and execute (0770).
	UserAndGroupReadWriteExecute Permissions = UserReadWriteExecute | GroupReadWriteExecute

	// OthersExecute is the execute bit for everyone else (0001).
	OthersExecute Permissions = 0001
	// OthersWrite is the write bit for everyone else (0002).
	OthersWrite Permissions = 0002
	// OthersRead is the read bit for everyone else (0004).
	OthersRead Permissions = 0004
	// OthersReadWrite lets everyone else read and write (0006).
	OthersReadWrite Permissions = OthersRead | OthersWrite
	// OthersReadWriteExecute lets everyone else read, write and execute (0007).
	OthersReadWriteExecute Permissions = OthersRead | OthersWrite | OthersExecute

	// AllRead lets owner, group and others read (0444).
	AllRead = UserRead | GroupRead | OthersRead
	// AllWrite lets owner, group and others write (0222).
	AllWrite = UserWrite | GroupWrite | OthersWrite
	// AllExecute lets owner, group and others execute (0111).
	AllExecute = UserExecute | GroupExecute | OthersExecute
	// AllReadWrite lets owner, group and others read and write (0666).
	AllReadWrite = UserReadWrite | GroupReadWrite | OthersReadWrite
)

// Permissions for a file, follows the Unix/os.FileMode bit schema.
type Permissions uint32

// FileMode returns an os.FileMode for the given permissions
// together with the information if the file is a directory.
// The setuid, setgid and sticky bits (04000, 02000, 01000)
// are mapped to os.ModeSetuid, os.ModeSetgid and os.ModeSticky.
func (perm Permissions) FileMode(isDir bool) os.FileMode {
	m := os.FileMode(perm & permBits)
	if perm&permSetuid != 0 {
		m |= os.ModeSetuid
	}
	if perm&permSetgid != 0 {
		m |= os.ModeSetgid
	}
	if perm&permSticky != 0 {
		m |= os.ModeSticky
	}
	if isDir {
		m |= os.ModeDir
	}
	return m
}

// Readable reports for owner, group and others if they can read.
func (perm Permissions) Readable() (user, group, others bool) {
	return perm&UserRead != 0, perm&GroupRead != 0, perm&OthersRead != 0
}

// Writable reports for owner, group and others if they can write.
func (perm Permissions) Writable() (user, group, others bool) {
	return perm&UserWrite != 0, perm&GroupWrite != 0, perm&OthersWrite != 0
}

// Executable reports for owner, group and others if they can execute.
func (perm Permissions) Executable() (user, group, others bool) {
	return perm&UserExecute != 0, perm&GroupExecute != 0, perm&OthersExecute != 0
}

// Can reports if all bits of p are set, so perm grants at least p.
func (perm Permissions) Can(p Permissions) bool {
	return perm&p == p
}

// JoinPermissions ORs the passed perms together,
// or returns defaultPerm if no perms are passed.
func JoinPermissions(perms []Permissions, defaultPerm Permissions) (result Permissions) {
	if len(perms) == 0 {
		return defaultPerm
	}
	for _, p := range perms {
		result |= p
	}
	return result
}

// OrDefault returns perm, or defaultPerm if perm is zero (NoPermissions).
// FileSystem implementations use it to apply their default permissions
// when a caller did not specify any.
func (perm Permissions) OrDefault(defaultPerm Permissions) Permissions {
	if perm == 0 {
		return defaultPerm
	}
	return perm
}

// PermissionsFromStdFileInfo returns the permission bits of a standard
// library [io/fs.FileInfo], dropping the file type bits of its mode.
// os.ModeSetuid, os.ModeSetgid and os.ModeSticky are mapped
// to the bits 04000, 02000 and 01000.
func PermissionsFromStdFileInfo(info iofs.FileInfo) Permissions {
	return PermissionsFromFileMode(info.Mode())
}

// PermissionsFromFileMode returns the permission bits of mode,
// dropping the file type bits. os.ModeSetuid, os.ModeSetgid and
// os.ModeSticky are mapped to the bits 04000, 02000 and 01000.
func PermissionsFromFileMode(mode os.FileMode) Permissions {
	perm := Permissions(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		perm |= permSetuid
	}
	if mode&os.ModeSetgid != 0 {
		perm |= permSetgid
	}
	if mode&os.ModeSticky != 0 {
		perm |= permSticky
	}
	return perm
}

// The setuid, setgid and sticky bits of Unix permissions
const (
	permSetuid Permissions = 0o4000
	permSetgid Permissions = 0o2000
	permSticky Permissions = 0o1000
)

// permBits are the read, write and execute bits of owner, group and others:
// the permissions without the setuid, setgid and sticky bits.
const permBits Permissions = 0o777
