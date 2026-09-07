module github.com/ungerik/go-fs/smbfs

go 1.26.0

replace github.com/ungerik/go-fs => ..

require github.com/ungerik/go-fs v0.0.0-00010101000000-000000000000 // replaced

require (
	github.com/hirochachacha/go-smb2 v1.1.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/geoffgarside/ber v1.1.0 // indirect
	github.com/pkg/xattr v0.4.12 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
