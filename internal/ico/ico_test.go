package ico

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// The scaffold's own icon, not a fixture shaped to fit the parser.
func TestLargestPNGOfTheScaffoldIcon(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "cli", "template", "static", "project", "assets", "icon.ico"))
	if err != nil {
		t.Fatal(err)
	}
	frame, ok := LargestPNG(data)
	if !ok {
		t.Fatal("no PNG frame found in the scaffold's icon.ico")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("the frame is not a decodable PNG: %v", err)
	}
	if cfg.Width < 128 || cfg.Width != cfg.Height {
		t.Fatalf("largest frame is %dx%d, want the square 128px-or-larger one", cfg.Width, cfg.Height)
	}
}

func TestLargestPNGRefusesWhatIsNotAnIcoOfPNGs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "cli", "template", "static", "project", "assets", "icon.ico"))
	if err != nil {
		t.Fatal(err)
	}
	frame, _ := LargestPNG(data)
	for name, in := range map[string][]byte{
		"empty":             nil,
		"a bare PNG":        frame,
		"header only":       data[:6],
		"directory cut off": data[:20],
	} {
		if _, ok := LargestPNG(in); ok {
			t.Errorf("%s: reported a frame", name)
		}
	}
	// An offset past the end must be skipped, not sliced.
	broken := append([]byte(nil), data...)
	for i := 0; i < int(broken[4]); i++ {
		broken[6+16*i+12] = 0xff
		broken[6+16*i+13] = 0xff
	}
	if _, ok := LargestPNG(broken); ok {
		t.Error("frames pointing past the end reported a frame")
	}
}
