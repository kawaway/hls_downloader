package hls_downloader

import (
	"testing"

	"github.com/likexian/gokit/assert"
)

func TestURI2basename(t *testing.T) {
	testcase := []struct {
		in  string
		exp string
	}{
		{
			in:  "http://localhost:8080",
			exp: "",
		},
		{
			in:  "http://localhost/aaa/playlist.m3u8",
			exp: "",
		},
	}

	for _, tt := range testcase {
		basename, err := uri2basename(tt.in)
		assert.Nil(t, err)
		assert.Equal(t, basename, tt.exp)
	}
}
