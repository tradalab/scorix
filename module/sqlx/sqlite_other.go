//go:build !(android && amd64)

package sqlx

func sqliteUnsupported() error { return nil }
