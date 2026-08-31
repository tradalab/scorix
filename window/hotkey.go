package window

type HotkeyRegistrar interface {
	RegisterHotkey(id int, mods, vk uint32, fn func()) error
}
