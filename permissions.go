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
func (perm Permissions) FileMode(isDir bool) os.FileMode {
	m := os.FileMode(perm)
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

// CanUserExecute reports if the owner can execute.
func (perm Permissions) CanUserExecute() bool { return perm.Can(UserExecute) }

// CanUserWrite reports if the owner can write.
func (perm Permissions) CanUserWrite() bool { return perm.Can(UserWrite) }

// CanUserRead reports if the owner can read.
func (perm Permissions) CanUserRead() bool { return perm.Can(UserRead) }

// CanUserReadWrite reports if the owner can read and write.
func (perm Permissions) CanUserReadWrite() bool { return perm.Can(UserReadWrite) }

// CanGroupExecute reports if the group can execute.
func (perm Permissions) CanGroupExecute() bool { return perm.Can(GroupExecute) }

// CanGroupWrite reports if the group can write.
func (perm Permissions) CanGroupWrite() bool { return perm.Can(GroupWrite) }

// CanGroupRead reports if the group can read.
func (perm Permissions) CanGroupRead() bool { return perm.Can(GroupRead) }

// CanGroupReadWrite reports if the group can read and write.
func (perm Permissions) CanGroupReadWrite() bool { return perm.Can(GroupReadWrite) }

// CanUserAndGroupRead reports if owner and group can read.
func (perm Permissions) CanUserAndGroupRead() bool { return perm.Can(UserAndGroupRead) }

// CanUserAndGroupReadWrite reports if owner and group can read and write.
func (perm Permissions) CanUserAndGroupReadWrite() bool { return perm.Can(UserAndGroupReadWrite) }

// CanOthersExecute reports if everyone else can execute.
func (perm Permissions) CanOthersExecute() bool { return perm.Can(OthersExecute) }

// CanOthersWrite reports if everyone else can write.
func (perm Permissions) CanOthersWrite() bool { return perm.Can(OthersWrite) }

// CanOthersRead reports if everyone else can read.
func (perm Permissions) CanOthersRead() bool { return perm.Can(OthersRead) }

// CanOthersReadWrite reports if everyone else can read and write.
func (perm Permissions) CanOthersReadWrite() bool { return perm.Can(OthersReadWrite) }

// CanAllRead reports if owner, group and others can read.
func (perm Permissions) CanAllRead() bool { return perm.Can(AllRead) }

// CanAllWrite reports if owner, group and others can write.
func (perm Permissions) CanAllWrite() bool { return perm.Can(AllWrite) }

// CanAllExecute reports if owner, group and others can execute.
func (perm Permissions) CanAllExecute() bool { return perm.Can(AllExecute) }

// CanAllReadWrite reports if owner, group and others can read and write.
func (perm Permissions) CanAllReadWrite() bool { return perm.Can(AllReadWrite) }

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
func PermissionsFromStdFileInfo(info iofs.FileInfo) Permissions {
	return Permissions(info.Mode().Perm())
}
