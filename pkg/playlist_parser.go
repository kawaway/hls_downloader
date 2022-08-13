package hls_downloader

import (
	"bufio"
	"net/url"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
)

type playlist struct {
	sequence       int
	targetDuration int
	end            bool
	segments       []*segment
	payload        string
}

func (p *playlist) modify(playlistURI *url.URL) error {
	for _, s := range p.segments {
		segmentURI, err := url.Parse(s.uri)
		if err != nil {
			return xerrors.Errorf("url.Parse failed: %w", err)
		}
		if !segmentURI.IsAbs() {
			// RelativeURI -> AbsoluteURL
			absSegmentURI := *playlistURI
			slice := strings.Split(absSegmentURI.Path, "/")
			slice[len(slice)-1] = segmentURI.Path
			absSegmentURI.Path = strings.Join(slice, "/")
			s.uri = absSegmentURI.String()
		}
	}
	return nil
}

type playlistParser struct {
	logger *zap.Logger
}

func (p *playlistParser) parse1st(sc *bufio.Scanner) (*url.URL, error) {
	var uri *url.URL
	for sc.Scan() {
		var err error
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		uri, err = url.Parse(line)
		if err != nil {
			return nil, xerrors.Errorf("url.Parse failed: +%w", err)
		}
	}
	if uri == nil {
		return nil, xerrors.New("playlist uri is not found")
	}
	return uri, nil
}

func (p *playlistParser) parse2nd(sc *bufio.Scanner) (*playlist, error) {
	var (
		mediaSequence   int
		targetDuration  int
		no              int
		segmentDuration float64
		end             bool
		segments        []*segment
	)
	segments = make([]*segment, 0, 10)

	for sc.Scan() {
		line := sc.Text()
		p.logger.Info("", zap.String("line", line))
		var err error
		// FIXME: EXT-X-MEDIA-SEQUENCE 等を受信したかどうかの状態を管理する
		if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE") {
			strs := strings.Split(line, ":")
			if len(strs) == 2 {
				mediaSequence, err = strconv.Atoi(strs[1])
				if err != nil {
					return nil, xerrors.Errorf("invalid playlist, %v :%+w", line, err)
				}
				no = mediaSequence
			} else {
				return nil, xerrors.Errorf("invalid playlist, %v", line)
			}
		} else if strings.HasPrefix(line, "#EXT-X-TARGETDURATION") {
			strs := strings.Split(line, ":")
			if len(strs) == 2 {
				targetDuration, err = strconv.Atoi(strs[1])
				if err != nil {
					return nil, xerrors.Errorf("invalid playlist, %v :%+w", line, err)
				}
			} else {
				return nil, xerrors.Errorf("invalid playlist, %v", line)
			}
		} else if strings.HasPrefix(line, "#EXTINF") {
			strs := strings.Split(line, ":")
			if len(strs) == 2 {
				strs2 := strings.Split(strs[1], ",")
				if len(strs2) == 2 {
					segmentDuration, err = strconv.ParseFloat(strs2[0], 64)
					if err != nil {
						return nil, xerrors.Errorf("invalid playlist, %v :%+w", line, err)
					}
					// TODO save a title = strs2[1]
				} else {
					return nil, xerrors.Errorf("invalid playlist, %v", line)
				}
			} else {
				return nil, xerrors.Errorf("invalid playlist, %v", line)
			}
			// FIXME wait segment
		} else if strings.HasPrefix(line, "#EXT-X-ENDLIST") {
			break
		} else if !strings.HasPrefix(line, "#") {
			// segment
			s := &segment{
				duration: segmentDuration,
				uri:      line,
				no:       no,
			}
			segments = append(segments, s)
			no++
		} else {
			// do nothing
		}
	}
	if len(segments) == 0 {
		return nil, xerrors.New("no segment")
	}
	return &playlist{
		sequence:       mediaSequence,
		targetDuration: targetDuration,
		end:            end,
		segments:       segments,
	}, nil
}
