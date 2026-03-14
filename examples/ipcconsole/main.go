package main

import (
	"context"
	"embed"
	"time"

	scorix "github.com/tradalab/scorix/kernel"
	browsermod "github.com/tradalab/scorix/module/browser"
	fsmod "github.com/tradalab/scorix/module/fs"
	gormmod "github.com/tradalab/scorix/module/gorm"
	storemod "github.com/tradalab/scorix/module/store"
	updatemod "github.com/tradalab/scorix/module/updater"
)

//go:embed frontend/*
var embeddedPublic embed.FS

//go:embed etc/app.yaml
var configFile []byte

type Args struct {
	User    string `json:"user"`
	Message string `json:"message"`
}

type Result struct {
	Message string `json:"message"`
}

func main() {
	app := scorix.MustNew(
		[]scorix.InitOption{
			scorix.WithConfigData(configFile),
		},
		scorix.WithAssets(embeddedPublic, "frontend"),
	)

	// Initialize and configure modules
	app.Modules().Register(fsmod.New())
	app.Modules().Register(browsermod.New())
	app.Modules().Register(storemod.New())
	app.Modules().Register(updatemod.New())
	app.Modules().Register(gormmod.New())

	app.Cmd().Handle("cmd-send", func(ctx context.Context, args Args) (Result, error) {
		return Result{Message: args.Message}, nil
	})

	app.Evt().On("event:send", func(ctx context.Context, args Args) {
		for i := 0; i < 5; i++ {
			time.Sleep(2 * time.Second)
			app.Evt().Emit(ctx, "", "event:send", Result{Message: args.Message})
		}
	})

	if err := app.Run(); err != nil {
		panic(err)
	}
}
