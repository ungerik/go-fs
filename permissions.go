package fs

import (
	iofs "io/fs"
	"os"
)

var (
	NoPermissions Permissions = 0

	UserExecute          Permissions = 0100
	UserWrite            Permissions = 0200
	UserRead             Permissions = 0400
	UserReadWrite        Permissions = UserRead | UserWrite
	UserReadWriteExecute Permissions = UserRead | UserWrite | UserExecute

	GroupExecute          Permissions = 0010
	GroupWrite            Permissions = 0020
	GroupRead             Permissions = 0040
	GroupReadWrite        Permissions = GroupRead | GroupWrite
	GroupReadWriteExecute Permissions = GroupRead | GroupWrite | GroupExecute

	UserAndGroupRead             Permissions = UserRead | GroupRead
	UserAndGroupReadWrite        Permissions = UserReadWrite | GroupReadWrite
	UserAndGroupReadWriteExecute Permissions = UserReadWriteExecute | GroupReadWriteExecute

	OthersExecute          Permissions = 0001
	OthersWrite            Permissions = 0002
	OthersRead             Permissions = 0004
	OthersReadWrite        Permissions = OthersRead | OthersWrite
	OthersReadWriteExecute Permissions = OthersRead | OthersWrite | OthersExecute

	AllRead      = UserRead | GroupRead | OthersRead
	AllWrite     = UserWrite | GroupWrite | OthersWrite
	AllExecute   = UserExecute | GroupExecute | OthersExecute
	AllReadWrite = UserReadWrite | GroupReadWrite | OthersReadWrite
)

// Permissions for a file, follows the Unix/os.FileMode bit schema.
type Permissions int

// FileMode returns an os.FileMode for the given permissions
// together with the information if the file is a directory.
func (perm Permissions) FileMode(isDir bool) os.FileMode {
	m := os.FileMode(perm)
	if isDir {
		m |= os.ModeDir
	}
	return m
}

func (perm Permissions) Readable() (user, group, others bool) {
	return perm&UserRead != 0, perm&GroupRead != 0, perm&OthersRead != 0
}

func (perm Permissions) Writable() (user, group, others bool) {
	return perm&UserWrite != 0, perm&GroupWrite != 0, perm&OthersWrite != 0
}

func (perm Permissions) Executable() (user, group, others bool) {
	return perm&UserExecute != 0, perm&GroupExecute != 0, perm&OthersExecute != 0
}

func (perm Permissions) Can(p Permissions) bool {
	return perm&p == p
}

func (perm Permissions) CanUserExecute() bool   { return perm.Can(UserExecute) }
func (perm Permissions) CanUserWrite() bool     { return perm.Can(UserWrite) }
func (perm Permissions) CanUserRead() bool      { return perm.Can(UserRead) }
func (perm Permissions) CanUserReadWrite() bool { return perm.Can(UserReadWrite) }

func (perm Permissions) CanGroupExecute() bool   { return perm.Can(GroupExecute) }
func (perm Permissions) CanGroupWrite() bool     { return perm.Can(GroupWrite) }
func (perm Permissions) CanGroupRead() bool      { return perm.Can(GroupRead) }
func (perm Permissions) CanGroupReadWrite() bool { return perm.Can(GroupReadWrite) }

func (perm Permissions) CanUserAndGroupRead() bool      { return perm.Can(UserAndGroupRead) }
func (perm Permissions) CanUserAndGroupReadWrite() bool { return perm.Can(UserAndGroupReadWrite) }

func (perm Permissions) CanOthersExecute() bool   { return perm.Can(OthersExecute) }
func (perm Permissions) CanOthersWrite() bool     { return perm.Can(OthersWrite) }
func (perm Permissions) CanOthersRead() bool      { return perm.Can(OthersRead) }
func (perm Permissions) CanOthersReadWrite() bool { return perm.Can(OthersReadWrite) }

func (perm Permissions) CanAllRead() bool      { return perm.Can(AllRead) }
func (perm Permissions) CanAllWrite() bool     { return perm.Can(AllWrite) }
func (perm Permissions) CanAllExecute() bool   { return perm.Can(AllExecute) }
func (perm Permissions) CanAllReadWrite() bool { return perm.Can(AllReadWrite) }

func JoinPermissions(perms []Permissions, defaultPerm Permissions) (result Permissions) {
	if len(perms) == 0 {
		return defaultPerm
	}
	for _, p := range perms {
		result |= p
	}
	return result
}

func PermissionsFromStdFileInfo(info iofs.FileInfo) Permissions {
	return Permissions(info.Mode().Perm())
}
