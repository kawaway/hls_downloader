package hls_downloader

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/likexian/gokit/assert"
	"github.com/stretchr/testify/require"
)

type fakePlaylistController struct {
	resChan chan<- downloadResult
}

func (c *fakePlaylistController) onDownload(no int, err error) {
	c.resChan <- downloadResult{no: no, err: err}
}

func TestSegmentDownloer(t *testing.T) {
	inputContains := []byte("a_ts_file_binary")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "video/MP2T")
		_, err := w.Write(inputContains)
		require.Equal(t, nil, err)
	}))
	defer ts.Close()
	tr := ts.Client().Transport

	no := 65535
	const chanSize = 3
	outputDir := "restResult"
	_ = os.Mkdir(outputDir, 0755)

	to := time.Duration(time.Millisecond * 10)
	resChan := make(chan downloadResult, chanSize)
	c := &fakePlaylistController{resChan: resChan}
	segReqChan := make(chan segment, chanSize)

	d := newSegmentDownloader(c, segReqChan, tr, to, outputDir, "mytoken")
	go func() {
		d.Run()
	}()
	defer close(segReqChan)

	basename := "stream_65535.ts"
	u := ts.URL + "/aaa/bbb/" + basename
	segReqChan <- segment{duration: 1, uri: u, no: no}

	outputPath := outputDir + "/" + basename
	defer func() { _ = os.Remove(outputPath) }()

	// estimate response
	r := <-resChan
	assert.Equal(t, r.no, no)
	assert.Equal(t, r.err, nil)

	// estimate created file
	createdContents, err := os.ReadFile(outputPath)
	assert.Nil(t, err)
	assert.Equal(t, createdContents, inputContains)
}

func TestSegmentDownloerStop(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	}))
	defer ts.Close()
	tr := ts.Client().Transport

	const chanSize = 3
	to := time.Duration(time.Millisecond * 10)
	resChan := make(chan downloadResult, chanSize)
	c := &fakePlaylistController{resChan: resChan}
	segReqChan := make(chan segment, chanSize)

	done := make(chan struct{}, 1)
	d := newSegmentDownloader(c, segReqChan, tr, to, "testResult", "mytoken")
	go func() {
		d.Run()
		close(done)
	}()

	close(segReqChan)

	stop := false
	select {
	case <-done:
		stop = true
	case <-time.After(time.Second):
	}
	assert.Equal(t, stop, true)
}

func TestSegmentDownloerError(t *testing.T) {
	testcase := []struct {
		name        string
		statusCode  int
		expectError error
	}{
		{
			statusCode: 500,
		},
		{
			statusCode:  400,
			expectError: ErrSegmentDownloadClientFactor,
		},
		{
			statusCode: 401,
		},
		{
			statusCode: 403,
		},
		{
			statusCode:  404,
			expectError: ErrSegmentDownloadClientFactor,
		},
		{
			name:        "too slow response",
			statusCode:  200,
			expectError: ErrSegmentDownloadTimeout,
		},
	}

	var i int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(testcase[i].statusCode)
		if testcase[i].name == "too slow response" {
			time.Sleep(time.Second)

			w.Header().Add("Content-Type", "video/MP2T")
			_, err := w.Write([]byte("a_ts_file_binary"))
			require.Equal(t, nil, err)
		}
		i++
	}))
	defer ts.Close()
	tr := ts.Client().Transport
	to := time.Duration(time.Millisecond * 10)
	const chanSize = 3

	for no, tt := range testcase {
		if tt.name != "" {
			fmt.Println("test ", tt.name)
		} else {
			fmt.Println("test response ", tt.statusCode)
		}

		resChan := make(chan downloadResult, chanSize)
		c := &fakePlaylistController{resChan: resChan}
		segReqChan := make(chan segment, chanSize)
		d := newSegmentDownloader(c, segReqChan, tr, to, "", "mytoken")
		go func() {
			d.Run()
		}()
		defer close(segReqChan)

		basename := fmt.Sprintf("stream_%d.ts", no)
		u := ts.URL + "/aaa/bbb/" + basename
		segReqChan <- segment{duration: 1, uri: u, no: no}

		defer func() { _ = os.Remove(basename) }()

		r := <-resChan
		assert.Equal(t, r.no, no)
		if tt.expectError != nil {
			assert.True(t, errors.Is(r.err, tt.expectError))
		} else {
			assert.NotNil(t, r.err)
		}
	}
}
