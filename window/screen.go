package window

type Screen struct {
	X       int     `json:"x"`
	Y       int     `json:"y"`
	W       int     `json:"w"`
	H       int     `json:"h"`
	Primary bool    `json:"primary"`
	Scale   float64 `json:"scale"` // 1.0 = 96 DPI
}

type ScreenLister interface {
	Screens() []Screen
}
