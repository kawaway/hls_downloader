package hls_downloader

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"golang.org/x/xerrors"
)

type segmentDownloaderDelegate interface {
	onDownload(int, error)
}

// segmentDownloader downloads segments and save it.
// input: please send a segment to inputChan.
// output: segmentDownloader reports a download result in a segmentDownloaderDelegate.
//   Please implements a segmentDownloaderDelegate interface.
// stop downloading: to exit the segmentDownloader, close the inputChan.
type segmentDownloader struct {
	delegate  segmentDownloaderDelegate
	inputChan <-chan segment
	cli       *client
	outputDir string
}

func newSegmentDownloader(sc segmentDownloaderDelegate, segChan <-chan segment, tr http.RoundTripper, to time.Duration, outputDir, token string) *segmentDownloader {
	return &segmentDownloader{
		delegate:  sc,
		inputChan: segChan,
		cli: &client{
			httpClient: &http.Client{
				Transport: tr,
				Timeout:   to,
			},
			token: token,
		},
		outputDir: outputDir,
	}
}

var (
	ErrSegmentDownloadTimeout      = xerrors.New("segment download timeout")
	ErrSegmentDownloadClientFactor = xerrors.New("segment download client factor")
)

// Run run the segmentDownloader
func (d *segmentDownloader) Run() {
	const errMax = 5
	var consecutiveErrCount int
	for {
		if consecutiveErrCount > errMax {
			// exit if a series of errors occur
			return
		}
		// wait inputChan
		seg, ok := <-d.inputChan
		if !ok {
			// exit if inputChan is closed
			return
		}
		resp, err := d.cli.get(seg.uri)
		if err != nil {
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				if urlErr.Timeout() {
					d.delegate.onDownload(seg.no, ErrSegmentDownloadTimeout)
					consecutiveErrCount++
					continue
				}
			} else if resp != nil {
				if resp.StatusCode == 401 || resp.StatusCode == 403 {
					d.delegate.onDownload(seg.no, xerrors.Errorf("get failed %v", resp.Status))
					return
				} else if ((resp.StatusCode >= 404) && (resp.StatusCode <= 499)) || resp.StatusCode == 400 || resp.StatusCode == 402 {
					d.delegate.onDownload(seg.no, xerrors.Errorf("statusCode: %s : %w", resp.Status, ErrSegmentDownloadClientFactor))
					consecutiveErrCount++
					continue
				} else if (resp.StatusCode >= 500) && (resp.StatusCode <= 599) {
					b, _ := io.ReadAll(resp.Body)
					d.delegate.onDownload(seg.no, xerrors.Errorf("get failed %v, %v", resp.Status, string(b)))
					return
				}
			}
			d.delegate.onDownload(seg.no, xerrors.Errorf("get failed +%w", err))
			return
		}

		basename, err := uri2SegmentPath(seg.uri, d.outputDir)
		if err != nil {
			d.delegate.onDownload(seg.no, xerrors.Errorf("uri2basename failed, seg.uri:%v, +%w", seg.uri, err))
			consecutiveErrCount++
		}
		f, err := os.Create(basename)
		if err != nil {
			d.delegate.onDownload(seg.no, xerrors.Errorf("os.Create failed, seg.uri:%v, +%w", seg.uri, err))
			consecutiveErrCount++
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(f, resp.Body)
		if err != nil {
			d.delegate.onDownload(seg.no, xerrors.Errorf("os.Copy failed, seg.uri:%v, +%w", seg.uri, err))
			consecutiveErrCount++
		}
		// success
		consecutiveErrCount = 0
		d.delegate.onDownload(seg.no, nil)
	}
}

func uri2SegmentPath(uri, outputDir string) (string, error) {
	basename, err := uri2basename(uri)
	if err != nil {
		return "", err
	}
	return outputDir + "/" + basename, nil
}
