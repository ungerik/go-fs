# uuiddir

```go
import "github.com/ungerik/go-fs/uuiddir"
```

Stores an unlimited number of UUID-named directories without ever putting too
many entries into one directory.

## Why it exists

File systems, and the tools and object-store listings around them, degrade
badly when a single directory holds millions of entries. An application that
keys its data by UUID produces that many easily.

`uuiddir` splits the 32 hex digits of a UUID into nested sub-directories, which
bounds the fan-out per level:

| Level | Hex digits | Max entries |
| ----- | ---------- | ----------- |
| 1     | 2          | 256         |
| 2     | 3          | 4096        |
| 3     | 3          | 4096        |
| 4     | 8          | —           |
| 5     | 16         | —           |

No directory grows without limit no matter how many UUIDs are stored. The
layout is deterministic, so a UUID maps to exactly one path and the path parses
back into the UUID.

```
f0498fad-437c-4954-ad82-8ec2cc202628
                 ↓
f0/498/fad/437c4954/ad828ec2cc202628
```

## UUID type

Every function takes a plain `[16]byte`, so the package pulls in no UUID
dependency of its own. Any UUID library whose type is defined as `[16]byte`
assigns to it directly, because Go allows a named type where its underlying
unnamed type is expected:

```go
// works with any library whose UUID type is a [16]byte
var id [16]byte = myLibrary.NewUUID()
dir := uuiddir.Join(baseDir, id)
```

`ParseString` and `Parse` validate what they read: the version nibble must be
1-8 (RFC 9562, which covers the RFC 4122 versions 1-5 plus 6, 7 and 8) and the
variant bits must be one of NCS, RFC 4122 or Microsoft. Anything else is an
error.

## Path functions

These are pure, they never touch a file system.

```go
func Split(uuid [16]byte) []string
```

The five hex strings of the layout.

```go
uuiddir.Split(id)
// []string{"f0", "498", "fad", "437c4954", "ad828ec2cc202628"}
```

```go
func FormatString(uuid [16]byte) string
```

The split parts joined with slashes. Inverse of `ParseString`.

```go
uuiddir.FormatString(id) // "f0/498/fad/437c4954/ad828ec2cc202628"
```

```go
func ParseString(uuidPath string) (uuid [16]byte, err error)
```

Parses a 36 character path like `FormatString` returns. Any other length is an
error, as is a path that is not valid hex or not a valid UUID.

```go
func Join(baseDir fs.File, uuid [16]byte, pathParts ...string) fs.File
```

The UUID directory below `baseDir`, with optional extra path parts appended.

```go
file := uuiddir.Join(baseDir, id, "document.pdf")
// baseDir/f0/498/fad/437c4954/ad828ec2cc202628/document.pdf
```

```go
func Parse(uuidDir fs.File) (uuid [16]byte, err error)
```

Reads the UUID back out of a directory path. It uses the **last 36 characters**
of the path, so it works on any path that ends with a UUID directory regardless
of what `baseDir` was. A path shorter than 36 characters is an error.

## File system functions

```go
func Make(baseDir fs.File, uuid [16]byte) (uuidDir fs.File, err error)
```

Creates all five directory levels, like `mkdir -p`. Returns the deepest
directory.

```go
func Enum(ctx context.Context, baseDir fs.File, callback func(uuidDir fs.File, uuid [16]byte) error) error
```

Calls `callback` for every directory below `baseDir` that represents a UUID.
It walks exactly five levels deep.

Entries that don't fit the layout are **skipped, not reported**: hidden
entries, files where a directory was expected, and leaf directories whose path
does not parse as a UUID. That makes it safe to run over a directory that also
holds unrelated files. Returning an error from the callback stops the walk and
returns that error.

```go
err := uuiddir.Enum(ctx, baseDir, func(dir fs.File, id [16]byte) error {
    fmt.Println(id, dir.Path())
    return nil
})
```

```go
func Remove(ctx context.Context, baseDir fs.File, uuid [16]byte) error
```

Removes the UUID's directory recursively, then every parent directory that is
now empty, stopping before `baseDir`. Without that cleanup the tree would fill
with empty skeleton directories over time.

```go
func RemoveDir(ctx context.Context, baseDir, uuidSubDir fs.File) error
```

The same, for a directory you already have. It returns an error if `uuidSubDir`
is not actually below `baseDir` or is on a different file system, so a wrong
argument cannot delete outside the tree. Passing `baseDir` itself is a no-op
returning nil.

## Worked example

```go
package main

import (
    "context"
    "fmt"

    fs "github.com/ungerik/go-fs"
    "github.com/ungerik/go-fs/uuiddir"
)

func main() {
    ctx := context.Background()

    baseDir, err := fs.MakeTempDir()
    if err != nil {
        panic(err)
    }
    defer baseDir.RemoveRecursive(ctx)

    // f0498fad-437c-4954-ad82-8ec2cc202628
    id := [16]byte{
        0xf0, 0x49, 0x8f, 0xad, 0x43, 0x7c, 0x49, 0x54,
        0xad, 0x82, 0x8e, 0xc2, 0xcc, 0x20, 0x26, 0x28,
    }

    fmt.Println(uuiddir.FormatString(id))
    // f0/498/fad/437c4954/ad828ec2cc202628

    dir, err := uuiddir.Make(baseDir, id)
    if err != nil {
        panic(err)
    }
    err = dir.Join("document.txt").WriteAllString(ctx, "content")
    if err != nil {
        panic(err)
    }

    err = uuiddir.Enum(ctx, baseDir, func(uuidDir fs.File, found [16]byte) error {
        fmt.Println(uuiddir.FormatString(found), uuidDir.Path())
        return nil
    })
    if err != nil {
        panic(err)
    }

    // Removes the UUID directory and prunes the now empty parents
    err = uuiddir.Remove(ctx, baseDir, id)
    if err != nil {
        panic(err)
    }
}
```

## Related

- [Getting started](tutorial-getting-started.md) — the `fs.File` API used above
- [Copy, move and compare](howto-copy-move-and-compare.md)
