// Command gofs runs file operations on every file system of go-fs:
// local files, HTTP, FTP, SFTP, SMB, WebDAV, S3, Azure Blob Storage
// and Dropbox, also between them, like a copy from S3 to SFTP:
//
//	gofs cp -r s3://my-bucket/reports sftp://alice@example.com/backup
//
// Arguments are local paths or URIs, see "gofs help".
// The remote file systems are configured with environment variables,
// "gofs env" lists them.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
)

// Errors that run turns into exit codes without printing them.
var (
	// errUsage is returned for invalid command line arguments
	// after the error and the usage have been printed
	errUsage = errors.New("usage error")

	// errDiffer is returned by cmp for files that differ
	errDiffer = errors.New("files differ")
)

// command is a subcommand of gofs.
type command struct {
	name     string
	synopsis string // the arguments after the name
	summary  string
	// run parses args with flags, which has the usage of the command,
	// and runs it
	run func(c *cli, ctx context.Context, flags *flag.FlagSet, args []string) error
}

var commands = []command{
	{"ls", "[-l] [-r] [-a] [-u] [URI...]", "list directories, or print the names of files", (*cli).ls},
	{"stat", "URI...", "print the information about files", (*cli).stat},
	{"cat", "URI...", "write the content of files to stdout", (*cli).cat},
	{"put", "URI", "write stdin to a file", (*cli).put},
	{"cp", "[-r] SOURCE... DEST", "copy files, with -r directories, also between file systems", (*cli).cp},
	{"mv", "SOURCE... DEST", "move or rename files and directories, also between file systems", (*cli).mv},
	{"rm", "[-r] [-f] URI...", "remove files, with -r directories with their content", (*cli).rm},
	{"mkdir", "[-p] URI...", "create directories", (*cli).mkdir},
	{"touch", "URI...", "create empty files or update their modification time", (*cli).touch},
	{"hash", "[-a ALGORITHM] URI...", "print the content hashes of files", (*cli).hash},
	{"cmp", "[-r] URI URI", "compare two files or directories, exit with 1 if they differ", (*cli).cmp},
	{"env", "", "list the environment variables that configure the file systems", (*cli).env},
	{"version", "", "print the version of gofs", (*cli).version},
}

const usageHeader = `gofs runs file operations on local files and on the remote file systems
of go-fs, also between them, like a copy from S3 to SFTP.

usage: gofs COMMAND [FLAGS] [ARGUMENTS]

commands:
`

const usageURIs = `
Run "gofs help COMMAND" or "gofs COMMAND -h" for the flags of a command.

Arguments are local paths or URIs. The file systems of the URIs connect on
first use with the configuration of the environment, see "gofs env".

  /local/path, file:///local/path
  http://host/path, https://host/path          read only
  ftp://[user@]host[:port]/path, ftps://...
  sftp://[user@]host[:port]/path
  smb://[user@]host[:port]/share/path
  webdav:///path                               on GOFS_WEBDAV_URL
  s3://bucket/key
  azblob://ACCOUNT.blob.core.windows.net/CONTAINER/path
  azblob:///CONTAINER/path                     in AZURE_STORAGE_ACCOUNT
  dropbox:///path                              of DROPBOX_ACCESS_TOKEN

Pass passwords with the environment rather than in URIs, which end up in the
shell history. Characters like '%', '?', '#' and space in URIs may be
percent-encoded, like "%20" for a space, but a '%' must be encoded as "%25".
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		// A second signal ends the process if the operation doesn't end
		<-ctx.Done()
		stop()
	}()
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	stop()
	os.Exit(code)
}

// cli runs the commands with the streams and the environment of run.
type cli struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	conn   *connector
}

// run runs the command of args and returns the exit code:
// 0 for success, 1 for an error, 2 for invalid arguments.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	out := bufio.NewWriter(stdout)
	defer out.Flush()
	c := &cli{stdin: stdin, stdout: out, stderr: stderr, conn: newConnector(getenv)}

	if len(args) == 0 {
		c.printUsage(stderr)
		return 2
	}
	name, args := args[0], args[1:]
	switch name {
	case "help", "-h", "-help", "--help":
		if len(args) == 0 {
			c.printUsage(out)
			return 0
		}
		name, args = args[0], []string{"-h"}
	}
	cmd, ok := findCommand(name)
	if !ok {
		fmt.Fprintf(stderr, "gofs: unknown command %q\n\n", name)
		c.printUsage(stderr)
		return 2
	}

	err := cmd.run(c, ctx, c.newFlags(cmd), args)
	err = errors.Join(err, c.conn.close(), out.Flush())
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		return 2
	case errors.Is(err, errDiffer):
		return 1
	}
	fmt.Fprintf(stderr, "gofs %s: %s\n", cmd.name, err)
	return 1
}

func findCommand(name string) (command, bool) {
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd, true
		}
	}
	return command{}, false
}

func (c *cli) printUsage(w io.Writer) {
	fmt.Fprint(w, usageHeader)
	for _, cmd := range commands {
		fmt.Fprintf(w, "  %-8s %s\n", cmd.name, cmd.summary)
	}
	fmt.Fprint(w, usageURIs)
}

// newFlags returns the flag set of cmd, which prints the usage of cmd.
func (c *cli) newFlags(cmd command) *flag.FlagSet {
	flags := flag.NewFlagSet(cmd.name, flag.ContinueOnError)
	flags.SetOutput(c.stderr)
	flags.Usage = func() {
		fmt.Fprintf(c.stderr, "usage: gofs %s %s\n\n%s\n", cmd.name, cmd.synopsis, cmd.summary)
		hasFlags := false
		flags.VisitAll(func(*flag.Flag) { hasFlags = true })
		if hasFlags {
			fmt.Fprintln(c.stderr, "\nflags:")
			flags.PrintDefaults()
		}
	}
	return flags
}

// parseFlags parses args with flags and checks that the number
// of the remaining arguments is between minArgs and maxArgs,
// a negative maxArgs allows any number.
// It returns errUsage after printing the usage for invalid arguments.
func parseFlags(flags *flag.FlagSet, args []string, minArgs, maxArgs int) error {
	err := flags.Parse(expandBoolFlags(flags, args))
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errUsage // Parse printed the error and the usage
	}
	n := flags.NArg()
	if n >= minArgs && (maxArgs < 0 || n <= maxArgs) {
		return nil
	}
	var expected string
	switch {
	case minArgs == maxArgs && minArgs == 0:
		expected = "no arguments"
	case minArgs == maxArgs:
		expected = fmt.Sprintf("%d arguments", minArgs)
	case maxArgs < 0:
		expected = fmt.Sprintf("at least %d arguments", minArgs)
	default:
		expected = fmt.Sprintf("%d to %d arguments", minArgs, maxArgs)
	}
	fmt.Fprintf(flags.Output(), "gofs %s: expected %s, got %d\n\n", flags.Name(), strings.Replace(expected, "1 arguments", "1 argument", 1), n)
	flags.Usage()
	return errUsage
}

// expandBoolFlags returns args with the combined boolean flags of flags
// before the first argument that is no flag expanded,
// like "-rf" to "-r" and "-f" as Unix commands accept them.
func expandBoolFlags(flags *flag.FlagSet, args []string) []string {
	var expanded []string
	for i, arg := range args {
		if arg == "--" || len(arg) < 2 || arg[0] != '-' {
			return append(expanded, args[i:]...)
		}
		names := arg[1:]
		if len(names) < 2 || strings.ContainsFunc(names, func(r rune) bool { return !isBoolFlag(flags, string(r)) }) {
			expanded = append(expanded, arg)
			continue
		}
		for _, name := range names {
			expanded = append(expanded, "-"+string(name))
		}
	}
	return expanded
}

// isBoolFlag reports if flags defines the boolean flag name.
func isBoolFlag(flags *flag.FlagSet, name string) bool {
	f := flags.Lookup(name)
	if f == nil {
		return false
	}
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

func (c *cli) version(ctx context.Context, flags *flag.FlagSet, args []string) error {
	err := parseFlags(flags, args, 0, 0)
	if err != nil {
		return err
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return errors.New("no build information")
	}
	fmt.Fprintf(c.stdout, "gofs %s (%s)\n", info.Main.Version, info.GoVersion)
	return nil
}
