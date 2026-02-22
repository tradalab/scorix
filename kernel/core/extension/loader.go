package extension

import (
	"context"

	"github.com/tradalab/scorix/kernel/internal/logger"
)

// LoadExtensions calls Init() for all extension
func LoadExtensions(ctx context.Context) error {
	for _, ext := range All() {
		if err := ext.Init(ctx); err != nil {
			logger.Info("[extension] failed init " + ext.Name() + ": " + err.Error())
			return err
		}
		logger.Info("[extension] loaded: " + ext.Name())
	}
	return nil
}

// StopExtensions calls Stop() in reverse order
func StopExtensions(ctx context.Context) {
	all := All()
	for i := len(all) - 1; i >= 0; i-- {
		if err := all[i].Stop(ctx); err != nil {
			logger.Info("[extension] failed stopping " + all[i].Name() + ": " + err.Error())
		} else {
			logger.Info("[extension] stopped: " + all[i].Name())
		}
	}
}
