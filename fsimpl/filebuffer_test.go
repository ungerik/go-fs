package fsimpl

import (
	"crypto/rand"
	"testing"
	"testing/iotest"
)

func TestFileBuffer(t *testing.T) {
	randBytes1000 := make([]byte, 1000)
	_, err := rand.Read(randBytes1000)
	if err != nil {
		panic(err)
	}

	testData := [][]byte{
		nil,
		{},
		{0},
		{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		randBytes1000,
	}
	for _, data := range testData {
		err = iotest.TestReader(NewFileBuffer(data), data)
		if err != nil {
			t.Fatal(err)
		}
		err = iotest.TestReader(NewReadonlyFileBuffer(data, nil), data)
		if err != nil {
			t.Fatal(err)
		}
	}
}
