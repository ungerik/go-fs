module github.com/ungerik/go-fs/azureblobfs

go 1.26.0

replace github.com/ungerik/go-fs => ..

require github.com/ungerik/go-fs v0.0.0-00010101000000-000000000000 // replaced

require (
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.8.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.22.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.12.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/pkg/xattr v0.4.12 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.38.0 // indirect
)
