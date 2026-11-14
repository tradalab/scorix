//go:build !server && windows

package systemtray

func trayHostReady() error { return nil }
