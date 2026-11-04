package main

import (
	"flag"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// gofs runs the command line args with env as the environment
// and returns the exit code and the output.
func gofs(t *testing.T, env map[string]string, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	code = run(t.Context(), args, strings.NewReader(stdin), &out, &errOut, func(name string) string { return env[name] })
	return code, out.String(), errOut.String()
}

func TestRun_Usage(t *testing.T) {
	code, stdout, stderr := gofs(t, nil, "")
	assert.Equal(t, 2, code, "no command")
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "commands:")

	code, stdout, _ = gofs(t, nil, "", "help")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "commands:")
	for _, cmd := range commands {
		assert.Contains(t, stdout, "  "+cmd.name+" ", "every command is listed")
	}

	code, _, stderr = gofs(t, nil, "", "help", "cp")
	assert.Equal(t, 0, code)
	assert.Contains(t, stderr, "usage: gofs cp [-r] SOURCE... DEST")
	assert.Contains(t, stderr, "-r\tcopy directories recursively")

	code, _, stderr = gofs(t, nil, "", "rm", "-h")
	assert.Equal(t, 0, code)
	assert.Contains(t, stderr, "usage: gofs rm")

	code, _, stderr = gofs(t, nil, "", "unknown")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, `unknown command "unknown"`)
}

func TestRun_InvalidArguments(t *testing.T) {
	code, _, stderr := gofs(t, nil, "", "put")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "gofs put: expected 1 argument, got 0")

	code, _, stderr = gofs(t, nil, "", "cmp", "a")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "gofs cmp: expected 2 arguments, got 1")

	code, _, stderr = gofs(t, nil, "", "cp", "a")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "gofs cp: expected at least 2 arguments, got 1")

	code, _, stderr = gofs(t, nil, "", "env", "a")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "gofs env: expected no arguments, got 1")

	code, _, stderr = gofs(t, nil, "", "ls", "-x")
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr, "flag provided but not defined: -x")
}

func TestExpandBoolFlags(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.Bool("r", false, "")
	flags.Bool("f", false, "")
	flags.String("a", "", "")
	for _, tc := range []struct {
		args, want []string
	}{
		{nil, nil},
		{[]string{"-rf", "x"}, []string{"-r", "-f", "x"}},
		{[]string{"-r", "-f"}, []string{"-r", "-f"}},
		{[]string{"-fr", "-rf"}, []string{"-f", "-r", "-r", "-f"}},
		// A string flag can't be combined
		{[]string{"-ra", "x"}, []string{"-ra", "x"}},
		{[]string{"-a", "rf"}, []string{"-a", "rf"}},
		// Arguments after the flags stay as they are
		{[]string{"x", "-rf"}, []string{"x", "-rf"}},
		{[]string{"--", "-rf"}, []string{"--", "-rf"}},
		{[]string{"-", "-rf"}, []string{"-", "-rf"}},
		{[]string{"--rf"}, []string{"--rf"}},
	} {
		assert.Equal(t, tc.want, expandBoolFlags(flags, tc.args), "%q", tc.args)
	}
}

func TestVersion(t *testing.T) {
	code, stdout, _ := gofs(t, nil, "", "version")
	assert.Equal(t, 0, code)
	assert.True(t, strings.HasPrefix(stdout, "gofs "), stdout)
}
