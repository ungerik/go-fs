module github.com/ungerik/go-fs/sftpfs

go 1.23

replace github.com/ungerik/go-fs => ..

require github.com/ungerik/go-fs v0.0.0-00010101000000-000000000000 // replaced

require (
	github.com/pkg/sftp v1.13.9
	github.com/stretchr/testify v1.10.0
	golang.org/x/crypto v0.37.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
