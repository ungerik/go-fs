module github.com/ungerik/go-fs/azureblobfs

go 1.26.0

replace github.com/ungerik/go-fs => ..

require github.com/ungerik/go-fs v0.0.0-00010101000000-000000000000 // replaced

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.23.3
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.8.2
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.13.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/storage/internal v0.1.0 // indirect
	github.com/apache/arrow-go/v18 v18.8.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/goccy/go-json v0.11.2 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/klauspost/cpuid/v2 v2.4.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/pkg/xattr v0.4.12 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/exp v0.0.0-20261014143641-f0251eb4aa3e // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)
