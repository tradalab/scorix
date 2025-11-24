package window

type Window interface {
	LoadHTML(html string)
	LoadURL(url string)
	Init(js string)
	Eval(js string)
	Close()
	Run()
	SetTitle(title string)
	SetSize(w, h int)
	Show()
	Hide()
	Center()
	Bind(name string, f interface{}) error
	Unbind(name string) error
}

type Config struct {
	Title  string `yaml:"title" json:"title"`
	Width  int    `yaml:"width" json:"width"`
	Height int    `yaml:"height" json:"height"`
	Debug  bool   `yaml:"debug" json:"debug"`
}
