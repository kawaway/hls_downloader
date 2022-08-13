package hls_downloader

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
)

type Config struct {
	URI                     string
	Token                   string
	OutputDir               string
	SegmentDownloadTimeout  time.Duration
	PlaylistDownloadTimeout time.Duration
}

type App struct {
	Config *Config
	Logger *zap.Logger
}

func NewApp(config *Config, logger *zap.Logger) (*App, error) {
	return &App{Config: config, Logger: logger}, nil
}

type client struct {
	httpClient *http.Client
	token      string
}

func (cli *client) get(uri string) (*http.Response, error) {
	req, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		return nil, xerrors.Errorf("http.NewRequest failed : %w", err)
	}
	req.Header.Add("authorization", fmt.Sprintf("bearer %v", cli.token))

	resp, err := cli.httpClient.Do(req)
	if err != nil {
		return resp, xerrors.Errorf("httpClient.Do failed : %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return resp, xerrors.Errorf("status code %v", resp.Status)
	}
	return resp, nil
}

type segment struct {
	duration float64
	uri      string
	no       int
}

type downloadResult struct {
	no  int
	err error
}

func uri2basename(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", xerrors.Errorf("url.Parse failed: %w", err)
	}
	return filepath.Base(u.Path), nil
}

// i.g. live/v1/playlist.m3u8, outputDir:out1, count:2 -> out1/playlist_2.m3u8
func uri2playlistPath(uri string, outputDir string, count int) (string, error) {
	basename, err := uri2basename(uri)
	if err != nil {
		return "", err
	}
	strs := strings.Split(basename, ".")
	// FIXME It won't work if there are more than two `. ` in the path does not work.
	if len(strs) != 2 {
		return "", xerrors.New("parse `.` dot error")
	}

	return fmt.Sprintf("%s/%s_%v.%s", outputDir, strs[0], count, strs[1]), nil
}

const channelCapa = 3

type streamingController struct {
	sequence           int
	downloadResultChan chan downloadResult
	config             *Config
	logger             *zap.Logger
}

func newStreamingController(c *Config, l *zap.Logger) *streamingController {
	return &streamingController{
		downloadResultChan: make(chan downloadResult, channelCapa),
		config:             c,
		logger:             l,
	}
}

func (sc *streamingController) Run(ctx context.Context) error {

	err := os.Mkdir(sc.config.OutputDir, 0755)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return xerrors.Errorf("Mkdir failed : %w", err)
	}

	// There shall be one transport to guarantee the order of requesting playlist and segment
	tr := http.DefaultTransport
	cli := &client{
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   sc.config.PlaylistDownloadTimeout,
		},
		token: sc.config.Token,
	}

	startTime := time.Now()
	timingInfo := sc.config.OutputDir + "/timing_info.txt"
	ft, err := os.Create(timingInfo)
	if err != nil {
		return xerrors.Errorf("os.Create failed: %w", err)
	}
	defer func() { _ = ft.Close() }()

	resp, err := cli.get(sc.config.URI)
	if err != nil {
		return xerrors.Errorf("cli.get failed: %w", err)
	}
	f1st, err := os.CreateTemp(sc.config.OutputDir, "playlist")
	if err != nil {
		return xerrors.Errorf("os.CreateTemp failed : %w", err)
	}
	defer func() {
		_ = f1st.Close()
		_ = os.Remove(f1st.Name())
	}()

	reader := io.TeeReader(resp.Body, f1st)

	s := bufio.NewScanner(reader)

	parser := playlistParser{logger: sc.logger}
	uri, err := parser.parse1st(s)
	if err != nil {
		return err
	}

	_, err = ft.Write([]byte(fmt.Sprintf("%d\n", time.Since(startTime).Milliseconds())))
	if err != nil {
		return xerrors.Errorf("Write failed: %w", err)
	}

	count := 0
	u, err := uri2playlistPath(uri.String(), sc.config.OutputDir, count)
	if err != nil {
		return err
	}

	err = os.Rename(f1st.Name(), u)
	if err != nil {
		return xerrors.Errorf("os.Rename failed : %w", err)
	}

	segChan := make(chan segment, channelCapa)

	// start downloader
	downloader := newSegmentDownloader(sc, segChan, tr, sc.config.SegmentDownloadTimeout, sc.config.OutputDir, sc.config.Token)
	go downloader.Run()
	defer close(segChan)

	var prevPlaylist *playlist
	var to time.Timer
	for count = 1; ; {
		if prevPlaylist != nil {
			// not first loop
			select {
			case r := <-sc.downloadResultChan:
				// download result is reported
				if r.err != nil {
					if !errors.Is(r.err, ErrSegmentDownloadTimeout) && !errors.Is(r.err, ErrSegmentDownloadClientFactor) {
						return r.err
					}
					sc.logger.Info("download failed", zap.Int("segment", r.no), zap.Error(r.err))
				}
				continue
			case <-ctx.Done():
				// canceled
				return nil
			case <-to.C:
				// This is the time to request playlist
			}
		}
		resp, err = cli.get(uri.String())
		if err != nil {
			return xerrors.Errorf("cli.get failed: %w", err)
		}
		f, err := os.CreateTemp(sc.config.OutputDir, "playlist")
		if err != nil {
			return xerrors.Errorf("os.CreateTemp failed : %w", err)
		}
		defer func() {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}()

		reader = io.TeeReader(resp.Body, f)

		newPlaylist, err := parser.parse2nd(bufio.NewScanner(reader))
		if err != nil {
			return err
		}

		err = newPlaylist.modify(uri)
		if err != nil {
			return err
		}
		// see RFC8216 6.3.4. Reloading the Media Playlist File
		nextDuration := time.Second * time.Duration(newPlaylist.targetDuration)
		if prevPlaylist != nil {
			if newPlaylist == prevPlaylist {
				// If there is no change in the playlist, do not acquire the segment and wait only for targetDuration
				to = *time.NewTimer(nextDuration)
				continue
			} else {
				// If there is a change in the playlist, acquire the segment and wait for targetDuration/2
				nextDuration /= 2
			}
		}

		u, err := uri2playlistPath(uri.String(), sc.config.OutputDir, count)
		if err != nil {
			return err
		}

		err = os.Rename(f.Name(), u)
		if err != nil {
			return xerrors.Errorf("os.Rename failed : %w", err)
		}

		_, err = ft.Write([]byte(fmt.Sprintf("%d\n", time.Since(startTime).Milliseconds())))
		if err != nil {
			return xerrors.Errorf("Write failed: %w", err)
		}

		// send segment to downloader
		for i := 0; i < len(newPlaylist.segments); i++ {
			segChan <- *newPlaylist.segments[i]
		}

		prevPlaylist = newPlaylist
		to = *time.NewTimer(nextDuration)
		count++
	}
}

func (sc *streamingController) onDownload(no int, err error) {
	sc.downloadResultChan <- downloadResult{no: no, err: err}
}

func (app *App) Run(ctx context.Context) error {
	controller := newStreamingController(app.Config, app.Logger)
	// TODO Even if an error returns, Run is performed again up to a certain number of errors
	err := controller.Run(ctx)
	if err != nil {
		app.Logger.Error("error", zap.Error(err))
	}
	return err
}
