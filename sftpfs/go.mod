module github.com/ungerik/go-fs/sftpfs

go 1.21.0

replace github.com/ungerik/go-fs => ../

require (
	github.com/pkg/sftp v1.13.7-0.20231120085349-5bdc2b0e679d
	github.com/stretchr/testify v1.8.4
	github.com/ungerik/go-fs v0.0.0-20231118104034-e3470c063fed
	golang.org/x/crypto v0.17.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fsnotify/fsnotify v1.7.0 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/sys v0.15.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
