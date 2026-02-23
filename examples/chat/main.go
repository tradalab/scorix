package main

import (
	"context"
	"embed"

	scorix "github.com/tradalab/scorix/kernel"
)

//go:embed frontend/*
var embeddedPublic embed.FS

//go:embed etc/app.yaml
var configFile []byte

type SendArgs struct {
	User    string `json:"user"`
	Message string `json:"message"`
}

type SendResult struct {
	Message string `json:"message"`
}

func main() {
	app := scorix.MustNew(
		[]scorix.InitOption{
			scorix.WithConfigData(configFile),
		},
		scorix.WithAssets(embeddedPublic, "frontend"),
	)

	app.Expose("send", func(ctx context.Context, args SendArgs) (SendResult, error) {
		return SendResult{Message: args.Message}, nil
	})

	if err := app.Run(); err != nil {
		panic(err)
	}
}
