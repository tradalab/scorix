package kernel

import (
	"io/fs"
	"os"

	"github.com/tradalab/scorix/kernel/core/config"
)

// ////////////////////////////////////////////////////////////////////////////////////////////////////
// InitOption

type InitOption func(*InitConfig)

type InitConfig struct {
	Path string
	Data []byte
}

func defaultInitConfig() *InitConfig {
	return &InitConfig{
		Path: "etc/app.yaml",
	}
}

func WithConfigFile(path string) InitOption {
	return func(c *InitConfig) {
		c.Path = path
		c.Data = nil
	}
}

func WithConfigData(data []byte) InitOption {
	return func(c *InitConfig) {
		c.Data = data
	}
}

// ////////////////////////////////////////////////////////////////////////////////////////////////////
// AppOption

type AppOption func(cfg *config.Config)

func WithAssets(assets fs.FS, path string) AppOption {
	return func(cfg *config.Config) {
		cfg.AssetFs = assets
		cfg.AssetFsPath = path
	}
}

func WithAssetsPath(path string) AppOption {
	return func(cfg *config.Config) {
		cfg.AssetFs = os.DirFS(path)
	}
}
