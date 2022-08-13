package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	hls_downloader "github.com/kawaway/hls_downloader/pkg"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	app := &cli.App{
		Name: "hls_downloader",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "uri",
				Usage:   "hls server URI",
				EnvVars: []string{"URI"},
			},
			&cli.StringFlag{
				Name:    "token",
				Usage:   "access token",
				EnvVars: []string{"TOKEN"},
			},
			&cli.Int64Flag{
				Name:    "timeout-segment",
				Usage:   "segment download timeout msec",
				Value:   2000,
				EnvVars: []string{"TIMEOUT_SEGMENT"},
			},
			&cli.Int64Flag{
				Name:    "timeout-playlist",
				Usage:   "playlist download timeout msec",
				Value:   6000,
				EnvVars: []string{"TIMEOUT_PLAYLIST"},
			},
			&cli.StringFlag{
				Name:    "out",
				Usage:   "output directory",
				Value:   "out",
				EnvVars: []string{"OUT"},
			},
		},
		Action: func(c *cli.Context) error {
			config := &hls_downloader.Config{
				URI:                     c.String("uri"),
				Token:                   c.String("token"),
				OutputDir:               c.String("out"),
				SegmentDownloadTimeout:  time.Millisecond * time.Duration(c.Int64("timeout-segment")),
				PlaylistDownloadTimeout: time.Millisecond * time.Duration(c.Int64("timeout-playlist")),
			}

			zc := zap.NewDevelopmentConfig()
			zc.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
			zc.OutputPaths = []string{"stdout"}
			logger, err := zc.Build()
			if err != nil {
				return err
			}
			ctx := context.Background()

			app, err := hls_downloader.NewApp(config, logger)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithCancel(ctx)

			go func() {
				signals := []os.Signal{syscall.SIGINT, syscall.SIGTERM}
				ch := make(chan os.Signal, 1)
				signal.Notify(ch, signals...)

				sig := <-ch

				app.Logger.Info("shutting down by signal", zap.Stringer("signal", sig))
				signal.Reset(signals...)
				cancel()
			}()

			return app.Run(ctx)
		},
	}
	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
