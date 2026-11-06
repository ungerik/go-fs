package main

import (
	"context"
	"crypto/md5" //#nosec G501 -- MD5 is only a content checksum for backward compatibility, see hashFuncs
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	fs "github.com/ungerik/go-fs"
	"github.com/ungerik/go-fs/fsimpl"
)

// timeFormat is the format of modification times in the output
const timeFormat = "2006-01-02 15:04:05"

// hashFuncs are the algorithms of the hash command
var hashFuncs = map[string]fs.ContentHashFunc{
	"dropbox": fsimpl.DropboxContentHash,
	"sha256":  fs.ContentHashFuncFrom(sha256.New),
	// MD5 is supported for backward compatibility with the checksums of
	// existing files and tools, like S3 ETags and md5sum lists.
	// It only compares contents and protects nothing, so the weak
	// hash is ok for security linters like gosec (G401, G501).
	"md5": fs.ContentHashFuncFrom(md5.New),
}

// files returns the files of uris.
func (c *cli) files(ctx context.Context, uris []string) ([]fs.File, error) {
	files := make([]fs.File, len(uris))
	for i, uri := range uris {
		file, err := c.conn.file(ctx, uri)
		if err != nil {
			return nil, err
		}
		files[i] = file
	}
	return files, nil
}

// ctxReader ends with the error of ctx once ctx is done,
// so that copies by io.Copy end between two reads.
// It checks ctx again after each read, because a read that was
// blocked when ctx got canceled can end with io.EOF, which would
// let io.Copy succeed with the data read so far.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, err
}

// listEntry is a file listed by ls with the name that ls prints.
type listEntry struct {
	name string
	info iofs.FileInfo
}

func (c *cli) ls(ctx context.Context, flags *flag.FlagSet, args []string) error {
	var (
		long      bool
		recursive bool
		all       bool
		uris      bool
	)
	flags.BoolVar(&long, "l", false, "print the permissions, size and modification time")
	flags.BoolVar(&recursive, "r", false, "list the files of the sub-directories recursively, without the directories")
	flags.BoolVar(&all, "a", false, "also list hidden files")
	flags.BoolVar(&uris, "u", false, "print the URIs of the files instead of their names")
	err := parseFlags(flags, args, 0, -1)
	if err != nil {
		return err
	}
	args = flags.Args()
	if len(args) == 0 {
		args = append(args, ".")
	}
	files, err := c.files(ctx, args)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(c.stdout, 0, 0, 0, ' ', tabwriter.AlignRight)
	for i, file := range files {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.IsDir() {
			printEntry(w, listEntry{string(file), info}, long)
			continue
		}
		var entries []listEntry
		add := func(info *fs.FileInfo) error {
			if info.IsHidden && !all {
				return nil
			}
			name := info.Name
			if recursive {
				rel, err := file.RelPathOf(info.File)
				if err == nil {
					if !all && inHiddenDir(rel) {
						// The listing descends into hidden directories
						return nil
					}
					name = rel
				}
			}
			if uris {
				name = string(info.File)
			}
			entries = append(entries, listEntry{name, info.StdFileInfo()})
			return nil
		}
		if recursive {
			err = file.ListDirInfoRecursive(ctx, add)
		} else {
			err = file.ListDirInfo(ctx, add)
		}
		if err != nil {
			return err
		}
		slices.SortFunc(entries, func(a, b listEntry) int { return strings.Compare(a.name, b.name) })
		if len(files) > 1 {
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "%s:\n", string(file))
		}
		for _, entry := range entries {
			printEntry(w, entry, long)
		}
	}
	return w.Flush()
}

// endsWithDotElem reports if the last element of the path of arg is
// "." or "..", also percent-encoded in a URI like ".../%2E%2E", because
// the file systems decode the path of a URI before they clean it.
func endsWithDotElem(arg string) bool {
	if _, rest, ok := cutScheme(arg); ok {
		arg = fsimpl.UnescapeURIPath(rest)
	}
	elems := strings.FieldsFunc(arg, isPathSeparator)
	return len(elems) > 0 && (elems[len(elems)-1] == "." || elems[len(elems)-1] == "..")
}

// inHiddenDir reports if one of the directories of the relative path rel
// has a name that begins with a dot, like ".git" of ".git/config".
func inHiddenDir(rel string) bool {
	dirs := strings.FieldsFunc(rel, isPathSeparator)
	if len(dirs) > 0 {
		dirs = dirs[:len(dirs)-1]
	}
	return slices.ContainsFunc(dirs, func(dir string) bool { return strings.HasPrefix(dir, ".") })
}

// isPathSeparator reports if r separates the elements of a path or URI.
func isPathSeparator(r rune) bool { return r == '/' || r == '\\' }

// printEntry prints the name of entry, in the long format of ls if long
// is true, with the cells of the mode and the size for a tabwriter.
func printEntry(w io.Writer, entry listEntry, long bool) {
	if !long {
		fmt.Fprintln(w, entry.name)
		return
	}
	fmt.Fprintf(w, "%s \t%d\t %s  %s\n", entry.info.Mode(), entry.info.Size(), entry.info.ModTime().Format(timeFormat), entry.name)
}

func (c *cli) stat(ctx context.Context, flags *flag.FlagSet, args []string) error {
	err := parseFlags(flags, args, 1, -1)
	if err != nil {
		return err
	}
	files, err := c.files(ctx, flags.Args())
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(c.stdout, 0, 0, 1, ' ', 0)
	for i, file := range files {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if i > 0 {
			fmt.Fprintln(w)
		}
		fileType := "file"
		if info.IsDir() {
			fileType = "directory"
		}
		fmt.Fprintf(w, "URI:\t%s\n", file.URI())
		fmt.Fprintf(w, "Name:\t%s\n", info.Name())
		fmt.Fprintf(w, "Type:\t%s\n", fileType)
		fmt.Fprintf(w, "Size:\t%d\n", info.Size())
		fmt.Fprintf(w, "Modified:\t%s\n", info.ModTime().Format(timeFormat+" -0700"))
		fmt.Fprintf(w, "Mode:\t%s\n", info.Mode())
	}
	return w.Flush()
}

func (c *cli) cat(ctx context.Context, flags *flag.FlagSet, args []string) error {
	err := parseFlags(flags, args, 1, -1)
	if err != nil {
		return err
	}
	files, err := c.files(ctx, flags.Args())
	if err != nil {
		return err
	}
	for _, file := range files {
		reader, err := file.OpenReader()
		if err != nil {
			return err
		}
		_, err = io.Copy(c.stdout, ctxReader{ctx, reader})
		err = errors.Join(err, reader.Close())
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *cli) put(ctx context.Context, flags *flag.FlagSet, args []string) error {
	err := parseFlags(flags, args, 1, 1)
	if err != nil {
		return err
	}
	file, err := c.conn.file(ctx, flags.Arg(0))
	if err != nil {
		return err
	}
	info, err := file.Stat()
	switch {
	case err == nil && info.IsDir():
		// Before stdin is read
		return fs.NewErrIsDirectory(file)
	case err == nil && isSpecialFile(file, info):
		// A named pipe or device is written, not replaced
		_, err = writeAll(ctx, file, c.stdin)
		return err
	case err == nil:
		return putReplace(ctx, file, fs.PermissionsFromStdFileInfo(info), false, c.stdin)
	case !errors.Is(err, os.ErrNotExist):
		// Only a file that doesn't exist is removed if writing fails
		return err
	}
	// Stat follows symbolic links, Lstat tells a dangling one, which is
	// replaced like an existing file instead of creating its target
	// through it, which a failed write would leave behind
	if linkInfo, lstatErr := file.Lstat(); lstatErr == nil && linkInfo.IsSymlink {
		return putReplace(ctx, file, 0, true, c.stdin)
	}
	// Like fs.CopyFile, which cp uses
	if dir := file.Dir(); !dir.Exists() {
		err = dir.MakeAllDirs()
		if err != nil {
			return err
		}
	}
	aborted, err := writeAll(ctx, file, c.stdin)
	if err != nil && !aborted {
		// Don't leave a partial new file
		return errors.Join(err, fs.RemoveErrDoesNotExist(file.Remove()))
	}
	return err
}

// isSpecialFile reports if the existing file with info is a named pipe,
// a socket or a device, which put writes into instead of replacing it.
// Like fs.CopyFile it tells them by the mode of a local file, because the
// FileInfo of go-fs reports other local files as irregular as well, like
// the placeholder of a OneDrive file on Windows.
func isSpecialFile(file fs.File, info iofs.FileInfo) bool {
	if localPath := file.LocalPath(); localPath != "" {
		localInfo, err := os.Stat(localPath)
		if err == nil {
			return localInfo.Mode()&(os.ModeNamedPipe|os.ModeSocket|os.ModeDevice|os.ModeCharDevice) != 0
		}
	}
	return !info.Mode().IsRegular()
}

// writeAll writes everything that r reads to file. If reading or writing
// fails, a writer that implements fs.WriteAborter discards the written data
// and aborted is true, other writers leave the data written so far.
func writeAll(ctx context.Context, file fs.File, r io.Reader) (aborted bool, err error) {
	writer, err := file.OpenWriter()
	if err != nil {
		return false, err
	}
	_, err = io.Copy(writer, ctxReader{ctx, r})
	if err != nil {
		if aborter, ok := writer.(fs.WriteAborter); ok {
			return true, errors.Join(err, aborter.Abort())
		}
		return false, errors.Join(err, writer.Close())
	}
	return false, writer.Close()
}

// putReplace writes everything that r reads to the existing regular file
// with the permissions perm, which keeps its content if reading or writing
// fails or ctx is canceled. A writer that implements fs.WriteAborter
// doesn't change the file before it is closed, so it writes the file
// directly. The writers of other file systems, like the local one and SFTP,
// truncate the file when they are opened, so a temporary file with the
// permissions perm is written in the same directory and moved over the file
// once it is complete, which renames it on the server without copying the
// data again. Where no temporary file can be created, like in a directory
// that only the file is writable in, the file is written directly.
// A dangling symbolic link, link true, is replaced with a file with the
// default permissions of a new file, and never written through, which would
// create its target, so perm is ignored and putReplace fails where it would
// write the file directly.
func putReplace(ctx context.Context, file fs.File, perm fs.Permissions, link bool, r io.Reader) error {
	errLink := func() error {
		return fmt.Errorf("can't replace the dangling symbolic link %s", fsimpl.RedactURI(string(file)))
	}
	temp := file.Dir().Join(".gofs-put-" + rand.Text())
	var tempPerm []fs.Permissions
	if !link {
		tempPerm = append(tempPerm, perm)
	}
	writer, err := temp.OpenWriter(tempPerm...)
	if errors.Is(err, os.ErrPermission) {
		// Opening can fail after the file was created,
		// like when an SFTP server refuses to set its permissions
		err = fs.RemoveErrDoesNotExist(temp.Remove())
		if err != nil {
			return err
		}
		if link {
			return errLink()
		}
		_, err = writeAll(ctx, file, r)
		return err
	}
	if err != nil {
		return err
	}
	if aborter, ok := writer.(fs.WriteAborter); ok {
		err = aborter.Abort()
		if err != nil {
			return err
		}
		if link {
			return errLink()
		}
		_, err = writeAll(ctx, file, r)
		return err
	}
	removeTemp := func(err error) error {
		return errors.Join(err, fs.RemoveErrDoesNotExist(temp.Remove()))
	}
	if !link {
		if perm&fs.UserWrite == 0 {
			// The file isn't writable, which the rename would ignore
			return removeTemp(errors.Join(fs.NewErrPermission(file), writer.Close()))
		}
		// OpenWriter applies the umask to the permissions of a new file.
		// File systems without permissions and mounts that refuse to change
		// them, like vfat, keep the permissions that the file got.
		err = temp.SetPermissions(perm)
		if err != nil && !errors.Is(err, errors.ErrUnsupported) && !errors.Is(err, os.ErrPermission) {
			return removeTemp(errors.Join(err, writer.Close()))
		}
	}
	_, err = io.Copy(writer, ctxReader{ctx, r})
	err = errors.Join(err, writer.Close())
	if err != nil {
		return removeTemp(err)
	}
	err = fs.Move(ctx, temp, file)
	if err != nil {
		if _, statErr := file.Lstat(); statErr != nil {
			// A failed move can remove the file before it renames
			// the temporary file, which then holds the only copy.
			// Lstat, because a dangling link that was kept has no Stat
			return fmt.Errorf("%w, the written data is in %s", err, string(temp))
		}
		return removeTemp(err)
	}
	return nil
}

// destination returns the files of the sources and the destination
// of the arguments of cp and mv, and if the sources go into the
// destination directory: for more than one source, or a destination
// with a trailing slash, which must be an existing directory then.
func (c *cli) destination(ctx context.Context, args []string) (sources []fs.File, dest fs.File, intoDir bool, err error) {
	files, err := c.files(ctx, args)
	if err != nil {
		return nil, "", false, err
	}
	sources, dest = files[:len(files)-1], files[len(files)-1]
	last := args[len(args)-1]
	isDir := dest.IsDir()
	intoDir = isDir || len(sources) > 1 || strings.HasSuffix(last, "/")
	if intoDir && !isDir {
		return nil, "", false, fmt.Errorf("%s is not a directory", fsimpl.RedactURI(string(dest)))
	}
	return sources, dest, intoDir, nil
}

func (c *cli) cp(ctx context.Context, flags *flag.FlagSet, args []string) error {
	var recursive bool
	flags.BoolVar(&recursive, "r", false, "copy directories recursively")
	err := parseFlags(flags, args, 2, -1)
	if err != nil {
		return err
	}
	sources, dest, intoDir, err := c.destination(ctx, flags.Args())
	if err != nil {
		return err
	}
	for _, src := range sources {
		if !src.IsDir() {
			// CopyFile copies into an existing directory itself
			err = fs.CopyFile(ctx, src, dest)
		} else if !recursive {
			err = fmt.Errorf("%s is a directory, copy it with -r", fsimpl.RedactURI(string(src)))
		} else if intoDir {
			var destDir fs.File
			destDir, err = dirEntry(dest, src)
			if err == nil {
				err = fs.CopyRecursive(ctx, src, destDir)
			}
		} else {
			err = fs.CopyRecursive(ctx, src, dest)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// dirEntry returns the entry of the directory dir with the name of src,
// like fs.CopyFile and fs.Move join it, and an error wrapping os.ErrInvalid
// if the name is not a single entry of dir: Join cleans the path, so a name
// like ".." or one with a separator of dir, like `..\x` of an S3 key for
// a local directory on Windows, would go outside of dir.
// fs.CopyRecursive validates the name of the entry that it returns.
func dirEntry(dir, src fs.File) (fs.File, error) {
	name := src.Name()
	entry := dir.Join(name)
	if name == "" || entry.Name() != name {
		return "", fmt.Errorf("%w: can't copy %s into %s, the name %q is not valid there", os.ErrInvalid, fsimpl.RedactURI(string(src)), fsimpl.RedactURI(string(dir)), name)
	}
	return entry, nil
}

func (c *cli) mv(ctx context.Context, flags *flag.FlagSet, args []string) error {
	err := parseFlags(flags, args, 2, -1)
	if err != nil {
		return err
	}
	sources, dest, _, err := c.destination(ctx, flags.Args())
	if err != nil {
		return err
	}
	for _, src := range sources {
		// Move moves into an existing directory itself
		err = fs.Move(ctx, src, dest)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *cli) rm(ctx context.Context, flags *flag.FlagSet, args []string) error {
	var (
		recursive bool
		force     bool
	)
	flags.BoolVar(&recursive, "r", false, "remove directories with their content")
	flags.BoolVar(&force, "f", false, "ignore files that don't exist")
	err := parseFlags(flags, args, 1, -1)
	if err != nil {
		return err
	}
	for _, arg := range flags.Args() {
		// Like rm, before anything is removed: the file systems clean
		// "dir/.." to the parent directory, which would be removed
		if endsWithDotElem(arg) {
			return fmt.Errorf("refusing to remove %s, a path that ends with '.' or '..'", fsimpl.RedactURI(arg))
		}
	}
	files, err := c.files(ctx, flags.Args())
	if err != nil {
		return err
	}
	for _, file := range files {
		if recursive {
			if !force {
				// RemoveRecursive succeeds for a missing file
				if err := file.CheckExists(); err != nil {
					return err
				}
			}
			err = file.RemoveRecursive(ctx)
		} else {
			err = file.Remove()
			if force {
				err = fs.RemoveErrDoesNotExist(err)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *cli) mkdir(ctx context.Context, flags *flag.FlagSet, args []string) error {
	var parents bool
	flags.BoolVar(&parents, "p", false, "create missing parent directories, no error if a directory exists")
	err := parseFlags(flags, args, 1, -1)
	if err != nil {
		return err
	}
	files, err := c.files(ctx, flags.Args())
	if err != nil {
		return err
	}
	for _, file := range files {
		switch {
		case parents:
			err = file.MakeAllDirs()
		case file.Exists():
			// MakeDir succeeds for an existing directory
			err = fs.NewErrAlreadyExists(file)
		default:
			err = file.MakeDir()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *cli) touch(ctx context.Context, flags *flag.FlagSet, args []string) error {
	err := parseFlags(flags, args, 1, -1)
	if err != nil {
		return err
	}
	files, err := c.files(ctx, flags.Args())
	if err != nil {
		return err
	}
	for _, file := range files {
		err = file.Touch()
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *cli) hash(ctx context.Context, flags *flag.FlagSet, args []string) error {
	var algorithm string
	flags.StringVar(&algorithm, "a", "dropbox", "the hash algorithm: dropbox, the content_hash of Dropbox, sha256 or md5")
	err := parseFlags(flags, args, 1, -1)
	if err != nil {
		return err
	}
	hashFunc, ok := hashFuncs[algorithm]
	if !ok {
		fmt.Fprintf(flags.Output(), "gofs hash: unknown algorithm %q\n\n", algorithm)
		flags.Usage()
		return errUsage
	}
	files, err := c.files(ctx, flags.Args())
	if err != nil {
		return err
	}
	for _, file := range files {
		hash, err := fs.FileContentHash(ctx, file, hashFunc)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "%s  %s\n", hash, string(file))
	}
	return nil
}

func (c *cli) cmp(ctx context.Context, flags *flag.FlagSet, args []string) error {
	var recursive bool
	flags.BoolVar(&recursive, "r", false, "compare the sub-directories of directories recursively")
	err := parseFlags(flags, args, 2, 2)
	if err != nil {
		return err
	}
	files, err := c.files(ctx, flags.Args())
	if err != nil {
		return err
	}
	a, b := files[0], files[1]
	var identical bool
	if a.IsDir() && b.IsDir() {
		identical, err = fs.IdenticalDirContents(ctx, a, b, recursive)
	} else {
		identical, err = fs.IdenticalFileContents(ctx, a, b)
	}
	if err != nil {
		return err
	}
	if !identical {
		fmt.Fprintf(c.stdout, "%s and %s differ\n", string(a), string(b))
		return errDiffer
	}
	return nil
}

func (c *cli) env(ctx context.Context, flags *flag.FlagSet, args []string) error {
	err := parseFlags(flags, args, 0, 0)
	if err != nil {
		return err
	}
	fmt.Fprint(c.stdout, "Environment variables of the file systems, \"set\" marks the ones that are set.\n")
	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	scheme := ""
	for _, v := range envVars {
		if v.scheme != scheme {
			scheme = v.scheme
			fmt.Fprintf(w, "\n%s\n", scheme)
		}
		set := ""
		if c.conn.getenv(v.name) != "" {
			set = "set"
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\n", v.name, set, v.help)
	}
	return w.Flush()
}
