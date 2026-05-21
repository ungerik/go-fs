package uuiddir

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	fs "github.com/ungerik/go-fs"
)

func Test_Split(t *testing.T) {
	uuid := mustParseUUID("f0498fad-437c-4954-ad82-8ec2cc202628")
	assert.Equal(t, []string{"f0", "498", "fad", "437c4954", "ad828ec2cc202628"}, Split(uuid), "Split(%s)", uuid)
}

func Test_Join(t *testing.T) {
	uuid := mustParseUUID("f0498fad-437c-4954-ad82-8ec2cc202628")

	baseDir := fs.File("/")
	uuidDir := Join(baseDir, uuid)
	expected := fs.File("/f0/498/fad/437c4954/ad828ec2cc202628")
	assert.Equal(t, expected, uuidDir, "Join")

	baseDir = fs.File("/my/base/dir")
	uuidDir = Join(baseDir, uuid)
	expected = fs.File("/my/base/dir/f0/498/fad/437c4954/ad828ec2cc202628")
	assert.Equal(t, expected, uuidDir, "Join")

	baseDir = fs.File("relativ/dir/")
	uuidDir = Join(baseDir, uuid)
	expected = fs.File("relativ/dir/f0/498/fad/437c4954/ad828ec2cc202628")
	assert.Equal(t, expected, uuidDir, "Join")
}

func Test_Parse(t *testing.T) {
	expected := [16]byte(mustParseUUID("f0498fad-437c-4954-ad82-8ec2cc202628"))
	dirs := []fs.File{
		"f0/498/fad/437c4954/ad828ec2cc202628",
		"f0/498/fad/437c4954/ad828ec2cc202628/",
		"/f0/498/fad/437c4954/ad828ec2cc202628",
		"/f0/498/fad/437c4954/ad828ec2cc202628/",
		"/my/base/dir/f0/498/fad/437c4954/ad828ec2cc202628",
		"/my/base/dir/f0/498/fad/437c4954/ad828ec2cc202628/",
		"relativ/dir/f0/498/fad/437c4954/ad828ec2cc202628",
		"relativ/dir/f0/498/fad/437c4954/ad828ec2cc202628/",
	}
	for _, dir := range dirs {
		uuid, err := Parse(dir)
		assert.NoError(t, err, "Parse(%q)", string(dir))
		assert.Equal(t, expected, uuid, "Parse(%q)", string(dir))
	}

	invalidDirs := []fs.File{
		"",
		"f0/498/fad/437c4954/ad828ec2cc20262",
		"f0/498/fad/437c4X54/ad828ec2cc202628",
		"f0/498/fad/437c4954/ad828ec2cc20/2628",
		"f0/498/fad/437c4954/ad828ec2cc202628 ",
		"/my/base/dir/f0/../498/fad/437c4954/ad828ec2cc202628",
		"relativ/dir/f0/498/fad/../437c4954/ad828ec2cc202628",
	}
	for _, dir := range invalidDirs {
		_, err := Parse(dir)
		assert.Error(t, err, "Parse(%q)", string(dir))
	}
}

func Test_parseUUID_versions(t *testing.T) {
	// RFC 9562 versions 1-8 are all valid.
	valid := []string{
		"f0498fad-437c-1954-ad82-8ec2cc202628", // v1
		"f0498fad-437c-2954-ad82-8ec2cc202628", // v2
		"f0498fad-437c-3954-ad82-8ec2cc202628", // v3
		"f0498fad-437c-4954-ad82-8ec2cc202628", // v4
		"f0498fad-437c-5954-ad82-8ec2cc202628", // v5
		"1ec9414c-232a-6b00-b3c8-9e6bdeced846", // v6
		"0190b6e0-8f3a-7c2d-9a1b-2c3d4e5f6a7b", // v7
		"0190b6e0-8f3a-8c2d-9a1b-2c3d4e5f6a7b", // v8
	}
	for _, str := range valid {
		_, err := parseUUID(str)
		assert.NoError(t, err, "parseUUID(%q)", str)
	}

	// Version 0 (Nil UUID) and versions 9-15 are not valid UUID versions.
	invalid := []string{
		"f0498fad-437c-0954-ad82-8ec2cc202628", // v0
		"0190b6e0-8f3a-9c2d-9a1b-2c3d4e5f6a7b", // v9
		"0190b6e0-8f3a-fc2d-9a1b-2c3d4e5f6a7b", // v15
	}
	for _, str := range invalid {
		_, err := parseUUID(str)
		assert.Error(t, err, "parseUUID(%q)", str)
	}
}

func Test_FormatString(t *testing.T) {
	uuid := mustParseUUID("f0498fad-437c-4954-ad82-8ec2cc202628")
	assert.Equal(t, "f0/498/fad/437c4954/ad828ec2cc202628", FormatString(uuid), "FormatString(%s)", uuid)
}

func Test_ParseString(t *testing.T) {
	expected := [16]byte(mustParseUUID("f0498fad-437c-4954-ad82-8ec2cc202628"))
	uuid, err := ParseString("f0/498/fad/437c4954/ad828ec2cc202628")
	assert.NoError(t, err, `ParseString("f0/498/fad/437c4954/ad828ec2cc202628")`)
	assert.Equal(t, expected, uuid, `ParseString("f0/498/fad/437c4954/ad828ec2cc202628")`)

	invalidStrings := []string{
		"",
		"f0/498/fad/437c4954/ad828ec2cc20262",
		"f0/498/fad/437c4X54/ad828ec2cc202628",
		"f0/498/fad/437c4954/ad828ec2cc20/2628",
		" f0/498/fad/437c4954/ad828ec2cc202628",
		"/f0/498/fad/437c4954/ad828ec2cc202628",
		"f0/498/fad/437c4954/ad828ec2cc202628 ",
		"f0/498/fad/437c4954/ad828ec2cc202628/",
		"/f0/498/fad/437c4954/ad828ec2cc202628/",
		// "f/0498/fad/437c4954/ad828ec2cc202628", // wrong slash position is not checked yet
	}
	for _, str := range invalidStrings {
		_, err = ParseString(str)
		assert.Error(t, err, "ParseString(%q)", str)
	}
}

func makeTestDirs() (baseDir fs.File, dirs map[fs.File]bool, ids map[[16]byte]struct{}, err error) {
	baseDir, err = fs.MakeTempDir()
	if err != nil {
		return "", nil, nil, err
	}

	dirs = map[fs.File]bool{
		baseDir.Join("ce", "d14", "f11", "83f64908", "b5028971ff464608"): true,
		baseDir.Join("8e", "7c4", "0d7", "49fa41e1", "8962263070ecb87f"): true,
		baseDir.Join("47", "17a", "9b7", "17d84c12", "89e1c998fb34e9ac"): true,
		baseDir.Join("10", "ba4", "b07", "907e4702", "a6df7b5df92c9c2e"): true,
		baseDir.Join("cc", "2f6", "bad", "9a2d4b12", "a08323a05e4207c2"): true,
		baseDir.Join("16", "5c4", "5c9", "2b1c4b4f", "ac66b6990c38d5df"): true,
		baseDir.Join("ae", "313", "95a", "c03a4962", "94fdbfe859f4d079"): true,
		baseDir.Join("a9", "14b", "4b1", "253048d3", "8a36470adc26101d"): true,
		baseDir.Join("d7", "8e3", "3ae", "dbfc4878", "9e9541644175f6c9"): true,
		baseDir.Join("3a", "7ed", "2cf", "a00d49ed", "bdf723534d292fcb"): true,
	}

	ids = map[[16]byte]struct{}{
		mustParseUUID("ced14f11-83f6-4908-b502-8971ff464608"): {},
		mustParseUUID("8e7c40d7-49fa-41e1-8962-263070ecb87f"): {},
		mustParseUUID("4717a9b7-17d8-4c12-89e1-c998fb34e9ac"): {},
		mustParseUUID("10ba4b07-907e-4702-a6df-7b5df92c9c2e"): {},
		mustParseUUID("cc2f6bad-9a2d-4b12-a083-23a05e4207c2"): {},
		mustParseUUID("165c45c9-2b1c-4b4f-ac66-b6990c38d5df"): {},
		mustParseUUID("ae31395a-c03a-4962-94fd-bfe859f4d079"): {},
		mustParseUUID("a914b4b1-2530-48d3-8a36-470adc26101d"): {},
		mustParseUUID("d78e33ae-dbfc-4878-9e95-41644175f6c9"): {},
		mustParseUUID("3a7ed2cf-a00d-49ed-bdf7-23534d292fcb"): {},
	}

	if len(ids) != len(dirs) {
		panic("len(ids) != len(dirs)")
	}

	for dir := range dirs {
		err := dir.MakeAllDirs()
		if err != nil {
			return "", nil, nil, err
		}
		if !dir.IsDir() {
			return "", nil, nil, fs.NewErrDoesNotExist(dir)
		}
	}

	return baseDir, dirs, ids, nil
}

func Test_Enum(t *testing.T) {
	baseDir, dirs, ids, err := makeTestDirs()
	assert.NoError(t, err, "makeTestDirs")
	defer baseDir.RemoveRecursive()

	Enum(t.Context(), baseDir, func(uuidDir fs.File, uuid [16]byte) error {
		hasDir := dirs[uuidDir] && uuidDir.IsDir()
		assert.True(t, hasDir, "valid directory")

		_, hasUUID := ids[uuid]
		assert.True(t, hasUUID, "valid UUID")

		return nil
	})
}

func findUUIDs(ctx context.Context, baseDir fs.File) map[[16]byte]struct{} {
	ids := make(map[[16]byte]struct{})
	Enum(ctx, baseDir, func(uuidDir fs.File, uuid [16]byte) error {
		ids[uuid] = struct{}{}
		return nil
	})
	return ids
}

func Test_RemoveDir(t *testing.T) {
	baseDir, _, ids, err := makeTestDirs()
	assert.NoError(t, err, "makeTestDirs")
	defer baseDir.RemoveRecursive()

	for id := range ids {
		idDir := Join(baseDir, id)
		assert.True(t, idDir.IsDir(), "test dir exists")
		err := RemoveDir(baseDir, idDir)
		assert.NoError(t, err, "RemoveDir")

		delete(ids, id)
		idsEqual(ids, findUUIDs(t.Context(), baseDir))
	}
}

func idsEqual(a, b map[[16]byte]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}
