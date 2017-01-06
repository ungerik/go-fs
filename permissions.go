package fs

var (
	UserExecute   Permissions = 0100
	UserWrite     Permissions = 0200
	UserRead      Permissions = 0400
	UserReadWrite             = UserRead + UserWrite

	GroupExecute   Permissions = 0010
	GroupWrite     Permissions = 0020
	GroupRead      Permissions = 0040
	GroupReadWrite             = GroupRead + GroupWrite

	UserGroupRead      = UserRead + GroupRead
	UserGroupReadWrite = UserReadWrite + GroupReadWrite

	OthersExecute   Permissions = 0001
	OthersWrite     Permissions = 0002
	OthersRead      Permissions = 0004
	OthersReadWrite             = OthersRead + OthersWrite

	AllRead      = UserRead + GroupRead + OthersRead
	AllWrite     = UserWrite + GroupWrite + OthersWrite
	AllExecute   = UserExecute + GroupExecute + OthersExecute
	AllReadWrite = UserReadWrite + GroupReadWrite + OthersReadWrite
)

type Permissions int

func (perm Permissions) Can(p Permissions) bool {
	return perm&p != 0
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

func CombinePermissions(perms []Permissions, defaultPerm Permissions) (result Permissions) {
	if len(perms) == 0 {
		return defaultPerm
	}
	for _, p := range perms {
		result |= p
	}
	return result
}
