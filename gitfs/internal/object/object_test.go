package object

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormat(t *testing.T) {
	format, err := FormatByName("")
	require.NoError(t, err)
	assert.Equal(t, SHA1.Name, format.Name)
	format, err = FormatByName("sha256")
	require.NoError(t, err)
	assert.Equal(t, 32, format.Size)
	_, err = FormatByName("md5")
	assert.Error(t, err)

	// Well known IDs of the empty blob and the empty tree
	assert.Equal(t, OID("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"), SHA1.Hash(TypeBlob, nil))
	assert.Equal(t, OID("4b825dc642cb6eb9a060e54bf8d69288fbee4904"), SHA1.Hash(TypeTree, nil))
	assert.Equal(t, OID("473a0f4c3be8a93681a267e3b1e9a7dcda1185436fe141f7749120a303721813"), SHA256.Hash(TypeBlob, nil))

	oid, err := SHA1.ParseOID("E69DE29BB2D1D6434B8B29AE775AD8C2E48C5391")
	require.NoError(t, err)
	assert.Equal(t, OID("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"), oid)
	assert.True(t, SHA1.IsOID(string(oid)))
	assert.False(t, SHA256.IsOID(string(oid)))
	assert.False(t, SHA1.IsOID("main"))
	assert.False(t, SHA1.IsOID("zz9de29bb2d1d6434b8b29ae775ad8c2e48c5391"))

	raw, err := oid.Raw()
	require.NoError(t, err)
	assert.Equal(t, oid, RawOID(raw))

	for typ, name := range map[Type]string{TypeCommit: "commit", TypeTree: "tree", TypeBlob: "blob", TypeTag: "tag", TypeOfsDelta: "ofs-delta", TypeRefDelta: "ref-delta", 5: "type(5)"} {
		assert.Equal(t, name, typ.String())
	}
}

func TestParseCommit(t *testing.T) {
	data := []byte("tree 23bff8000879b8f1bb06cf4293869b740597030f\n" +
		"parent 02ef002aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"author A U Thor <a@example.com> 1793019269 +0100\n" +
		"committer C O Mitter <c@example.com> 1793019270 -0230\n" +
		"\n" +
		"tree in the message is ignored\n")
	commit, err := SHA1.ParseCommit(data)
	require.NoError(t, err)
	assert.Equal(t, OID("23bff8000879b8f1bb06cf4293869b740597030f"), commit.Tree)
	assert.True(t, commit.Committed.Equal(time.Unix(1793019270, 0)))
	_, offset := commit.Committed.Zone()
	assert.Equal(t, -(2*3600 + 30*60), offset)

	for _, invalid := range []string{
		"",
		"tree xyz\n",
		"tree 23bff8000879b8f1bb06cf4293869b740597030f\ntree 23bff8000879b8f1bb06cf4293869b740597030f\n",
		"tree 23bff8000879b8f1bb06cf4293869b740597030f\ncommitter no time\n",
		"tree 23bff8000879b8f1bb06cf4293869b740597030f\ncommitter C <c> 1 2 3\n",
		"tree 23bff8000879b8f1bb06cf4293869b740597030f\ncommitter C <c> x +0000\n",
		"tree 23bff8000879b8f1bb06cf4293869b740597030f\ncommitter C <c> 1 0000\n",
		"tree 23bff8000879b8f1bb06cf4293869b740597030f\ncommitter C <c> 1 +00x0\n",
	} {
		_, err := SHA1.ParseCommit([]byte(invalid))
		assert.Error(t, err, invalid)
	}
}

func TestParseTag(t *testing.T) {
	tag, err := SHA1.ParseTag([]byte("object 64e9625c74cec658bb1fc0cb64e34a58cdddb9cf\ntype commit\ntag v1\n\nmsg\n"))
	require.NoError(t, err)
	assert.Equal(t, OID("64e9625c74cec658bb1fc0cb64e34a58cdddb9cf"), tag.Object)
	assert.Equal(t, "commit", tag.Type)

	_, err = SHA1.ParseTag([]byte("type commit\n"))
	assert.Error(t, err)
	_, err = SHA1.ParseTag([]byte("object nope\ntype commit\n"))
	assert.Error(t, err)
}

func TestParseTree(t *testing.T) {
	raw, err := SHA1.Hash(TypeBlob, nil).Raw()
	require.NoError(t, err)
	data := append([]byte("100755 run.sh\x00"), raw...)
	data = append(data, []byte("40000 dir\x00")...)
	data = append(data, raw...)
	entries, err := SHA1.ParseTree(data)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, TreeEntry{Mode: ModeExecutable, Name: "run.sh", OID: SHA1.Hash(TypeBlob, nil)}, entries[0])
	assert.Equal(t, uint32(ModeDir), entries[1].Mode)

	entries, err = SHA1.ParseTree(nil)
	require.NoError(t, err)
	assert.Empty(t, entries)

	for _, invalid := range [][]byte{
		[]byte("100644"),
		[]byte("1x0644 a\x00"),
		[]byte("100644 a"),
		[]byte("100644 a\x00short"),
	} {
		_, err := SHA1.ParseTree(invalid)
		assert.Error(t, err, string(invalid))
	}
}
