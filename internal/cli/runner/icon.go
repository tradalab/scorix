package runner

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"golang.org/x/image/draw"
)

type IconOptions struct {
	// Source icon: .svg (rendered) or .png (decoded). Required.
	Source string
	// OutDir for generated assets (default: dir of Source).
	OutDir string
	// PNG sizes to emit (default: 16,32,48,128,256,512,1024).
	Sizes []int
	// ICO bundles the listed sizes (≤256) into one Windows .ico. Empty → skip.
	ICOSizes []int
}

var (
	defaultIconSizes = []int{16, 32, 48, 128, 256, 512, 1024}
	defaultICOSizes  = []int{16, 32, 48, 128, 256}
)

func GenerateIcon(_ context.Context, opt IconOptions) error {
	if opt.Source == "" {
		return fmt.Errorf("icon: --source is required (.svg or .png)")
	}
	src, err := filepath.Abs(opt.Source)
	if err != nil {
		return err
	}
	outDir := opt.OutDir
	if outDir == "" {
		outDir = filepath.Dir(src)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	sizes := opt.Sizes
	if len(sizes) == 0 {
		sizes = defaultIconSizes
	}

	// Render at the largest requested size and downscale (Catmull-Rom) so every
	// output comes from one high-quality master.
	max := 0
	for _, s := range sizes {
		if s > max {
			max = s
		}
	}
	master, err := loadMaster(src, max)
	if err != nil {
		return err
	}

	fmt.Printf("==> Generating icons from %s\n", filepath.Base(src))
	pngBySize := map[int]string{}
	for _, size := range sizes {
		dst := image.NewRGBA(image.Rect(0, 0, size, size))
		draw.CatmullRom.Scale(dst, dst.Bounds(), master, master.Bounds(), draw.Over, nil)
		name := filepath.Join(outDir, fmt.Sprintf("icon-%d.png", size))
		if err := writePNG(name, dst); err != nil {
			return err
		}
		pngBySize[size] = name
		fmt.Printf("      %s\n", filepath.Base(name))
	}

	icoSizes := opt.ICOSizes
	if icoSizes == nil {
		icoSizes = defaultICOSizes
	}
	if len(icoSizes) > 0 {
		icoPath := filepath.Join(outDir, "icon.ico")
		if err := writeICO(icoPath, master, icoSizes); err != nil {
			return fmt.Errorf("write .ico: %w", err)
		}
		fmt.Printf("      %s\n", filepath.Base(icoPath))
	}

	fmt.Println("==> Icon generation complete!")
	return nil
}

func loadMaster(src string, size int) (image.Image, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	switch strings.ToLower(filepath.Ext(src)) {
	case ".svg":
		icon, err := oksvg.ReadIconStream(f)
		if err != nil {
			return nil, fmt.Errorf("parse svg: %w", err)
		}
		icon.SetTarget(0, 0, float64(size), float64(size))
		rgba := image.NewRGBA(image.Rect(0, 0, size, size))
		icon.Draw(rasterx.NewDasher(size, size,
			rasterx.NewScannerGV(size, size, rgba, rgba.Bounds())), 1.0)
		return rgba, nil
	case ".png":
		return png.Decode(f)
	default:
		return nil, fmt.Errorf("unsupported source %q (want .svg or .png)", filepath.Ext(src))
	}
}

func writePNG(path string, img image.Image) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	return png.Encode(out, img)
}

func scaled(master image.Image, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), master, master.Bounds(), draw.Over, nil)
	return dst
}

// writeICO bundles PNG-encoded frames into a Windows .ico (Vista+ PNG-in-ICO).
func writeICO(path string, master image.Image, sizes []int) error {
	sort.Ints(sizes)

	type frame struct {
		size int
		data []byte
	}
	var frames []frame
	for _, s := range sizes {
		if s > 256 {
			continue // ICO dimension byte is 0..255 (0 means 256); larger can't be encoded
		}
		var buf bytesBuffer
		if err := png.Encode(&buf, scaled(master, s)); err != nil {
			return err
		}
		frames = append(frames, frame{s, buf.Bytes()})
	}
	if len(frames) == 0 {
		return fmt.Errorf("no ICO frames ≤256px to write")
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	// ICONDIR header: reserved(0), type(1=icon), count.
	hdr := []byte{0, 0, 1, 0, byte(len(frames)), byte(len(frames) >> 8)}
	if _, err := out.Write(hdr); err != nil {
		return err
	}
	offset := 6 + 16*len(frames)
	for _, fr := range frames {
		dim := byte(fr.size)
		if fr.size >= 256 {
			dim = 0
		}
		n := len(fr.data)
		entry := []byte{
			dim, dim, 0, 0, // width, height, colors, reserved
			1, 0, 32, 0, // planes(1), bitcount(32)
			byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24),
			byte(offset), byte(offset >> 8), byte(offset >> 16), byte(offset >> 24),
		}
		if _, err := out.Write(entry); err != nil {
			return err
		}
		offset += n
	}
	for _, fr := range frames {
		if _, err := out.Write(fr.data); err != nil {
			return err
		}
	}
	return nil
}

// bytesBuffer is a minimal io.Writer over a growing slice (avoids importing
// bytes; png.Encode only needs Write).
type bytesBuffer struct{ b []byte }

func (w *bytesBuffer) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *bytesBuffer) Bytes() []byte               { return w.b }
