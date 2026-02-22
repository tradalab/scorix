package gormext

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tradalab/scorix/kernel/core/extension"
	"github.com/tradalab/scorix/kernel/core/extensions/fs"
	"github.com/tradalab/scorix/kernel/internal/logger"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type GormExt struct {
	db *gorm.DB
}

func (e *GormExt) Name() string {
	return "gorm"
}

func (e *GormExt) Init(ctx context.Context) error {
	logger.Info("[gorm] init")

	// Get config from context
	dbPath := "app.dat"
	if v, ok := extension.GetConfigPath(ctx, "extensions.gorm.dsn"); ok {
		dbPath = v.(string)
	}

	// FS
	fext, ok := extension.GetExt[*fs.FSExt]("fs")
	if !ok {
		panic("fs extension not found")
	}
	dsn := filepath.Join(fext.DataDir(), dbPath)
	if err := os.MkdirAll(filepath.Dir(dsn), 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}

	// Create db instance
	dialect := sqlite.Open(dsn)
	db, err := gorm.Open(dialect, &gorm.Config{PrepareStmt: true})
	if err != nil {
		return err
	}
	e.db = db

	logger.Info("[gorm] connected to sqlite: " + dbPath)

	extension.Expose(e, "Query")
	extension.Expose(e, "DB")

	return nil
}

func (e *GormExt) Stop(ctx context.Context) error {
	logger.Info("[gorm] stop")

	if e.db == nil {
		return nil
	}

	sqlDB, err := e.db.DB()
	if err == nil {
		sqlDB.Close()
	}

	return nil
}

func (e *GormExt) DB() *gorm.DB {
	return e.db
}

func (e *GormExt) Query(ctx context.Context, sql string) ([]map[string]interface{}, error) {
	rows, err := e.db.Raw(sql).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []map[string]interface{}{}
	cols, _ := rows.Columns()

	for rows.Next() {
		colsData := make([]interface{}, len(cols))
		colsPtr := make([]interface{}, len(cols))

		for i := range colsData {
			colsPtr[i] = &colsData[i]
		}

		if err := rows.Scan(colsPtr...); err != nil {
			return nil, err
		}

		rowMap := make(map[string]interface{})
		for i, col := range cols {
			rowMap[col] = colsData[i]
		}

		result = append(result, rowMap)
	}
	return result, nil
}

func init() {
	extension.Register(&GormExt{})
}
