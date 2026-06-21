package media

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePNG renders a WxH PNG of photographic-style content: smooth color
// gradients plus subtle per-pixel noise. That mix keeps PNG heavy (noise
// defeats its predictors) while JPEG compresses it cleanly — exactly the regime
// of a real generated photo, where the JPEG re-encode wins. Returns its path.
func writePNG(t *testing.T, w, h int, alpha bool) string {
	t.Helper()
	var seed uint32 = 12345
	noise := func() float64 { // deterministic LCG, range ~[-15, 15]
		seed = seed*1664525 + 1013904223
		return (float64(seed>>8&0xff)/255.0 - 0.5) * 30
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if alpha && (x+y)%7 == 0 {
				a = 0
			}
			n := noise()
			r := uint8(float64(x*255/w) + n)
			g := uint8(float64(y*255/h) + n)
			b := uint8(128 + n)
			img.Set(x, y, color.RGBA{r, g, b, a})
		}
	}
	p := filepath.Join(t.TempDir(), "in.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func dims(t *testing.T, path string) (int, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height
}

func TestOptimizeImage_OpaquePNGShrinksKeepsResolution(t *testing.T) {
	in := writePNG(t, 1200, 1600, false)
	inInfo, _ := os.Stat(in)

	out := OptimizeImage(in)
	if out == in {
		t.Fatal("expected a re-encoded jpeg path, got the original")
	}
	defer os.Remove(out)
	if !strings.HasSuffix(out, ".jpg") {
		t.Fatalf("expected .jpg output, got %s", out)
	}

	outInfo, _ := os.Stat(out)
	if outInfo.Size() >= inInfo.Size() {
		t.Fatalf("expected smaller file: in=%d out=%d", inInfo.Size(), outInfo.Size())
	}

	iw, ih := dims(t, in)
	ow, oh := dims(t, out)
	if iw != ow || ih != oh {
		t.Fatalf("resolution changed: %dx%d -> %dx%d", iw, ih, ow, oh)
	}
}

func TestOptimizeImage_TransparentPNGKept(t *testing.T) {
	in := writePNG(t, 400, 400, true)
	if out := OptimizeImage(in); out != in {
		os.Remove(out)
		t.Fatalf("transparent PNG should be left as-is, got %s", out)
	}
}

func TestOptimizeImage_SmallJPEGUntouched(t *testing.T) {
	// A path that doesn't exist but has a .jpg ext under 2 MiB threshold:
	// stat fails -> returns original. Use a real tiny jpeg instead.
	src := writePNG(t, 50, 50, false)
	jpgOut := OptimizeImage(src) // re-encodes (png) -> jpg
	// Move it to a .jpg path and feed it back: should be left untouched (small).
	small := filepath.Join(t.TempDir(), "small.jpg")
	data, _ := os.ReadFile(jpgOut)
	os.Remove(jpgOut)
	if err := os.WriteFile(small, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if out := OptimizeImage(small); out != small {
		os.Remove(out)
		t.Fatalf("small jpeg should be untouched, got %s", out)
	}
}
