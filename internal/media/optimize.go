package media

// Image optimization for outbound delivery. Higgsfield and other tools drop
// large 2k PNGs (often 10-11 MB) into the outbox; Telegram's sendPhoto rejects
// anything over 10 MiB and silently drops it. Re-encoding to high-quality JPEG
// at full resolution strips ~90% of the bytes with no visible quality loss and
// no change in pixel dimensions, so the photo arrives inline every time.

import (
	"image"
	"image/jpeg"
	_ "image/png" // register PNG decoder for image.Decode
	"os"
	"path/filepath"
	"strings"
)

// PhotoMaxBytes is Telegram's hard upper bound for sendPhoto uploads. Anything
// larger must go out as a document (which allows up to 50 MiB) instead.
const PhotoMaxBytes = 10 << 20 // 10 MiB

// jpegQuality is high enough to be visually lossless for photos while still
// shrinking files dramatically.
const jpegQuality = 92

// OptimizeImage returns a path suitable for sending. PNG/JPEG images without
// transparency are re-encoded to JPEG at full resolution into a temp file; the
// caller should delete the returned path if it differs from the input. On any
// failure (unsupported format, decode error, no size win) the original path is
// returned unchanged so delivery still proceeds.
func OptimizeImage(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch ext {
	case "png", "jpg", "jpeg":
	default:
		return path // webp/gif/etc: leave untouched (may be animated/transparent)
	}

	info, err := os.Stat(path)
	if err != nil {
		return path
	}
	// Already-small JPEGs aren't worth re-encoding.
	if (ext == "jpg" || ext == "jpeg") && info.Size() < 2<<20 {
		return path
	}

	f, err := os.Open(path)
	if err != nil {
		return path
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return path
	}

	// JPEG can't hold an alpha channel; keep transparent PNGs as-is.
	if ext == "png" && hasAlpha(img) {
		return path
	}

	out, err := os.CreateTemp("", "zoro-opt-*.jpg")
	if err != nil {
		return path
	}
	defer out.Close()
	if err := jpeg.Encode(out, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		os.Remove(out.Name())
		return path
	}
	// Only use the re-encoded copy if it actually saved bytes.
	if oi, err := os.Stat(out.Name()); err != nil || oi.Size() >= info.Size() {
		os.Remove(out.Name())
		return path
	}
	return out.Name()
}

// hasAlpha samples the image on a coarse grid and reports whether any sampled
// pixel is non-opaque. Cheap enough to run on every image; exact enough to
// avoid flattening a transparent PNG onto a black JPEG background.
func hasAlpha(img image.Image) bool {
	b := img.Bounds()
	stepX := max(b.Dx()/64, 1)
	stepY := max(b.Dy()/64, 1)
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			if _, _, _, a := img.At(x, y).RGBA(); a < 0xffff {
				return true
			}
		}
	}
	return false
}
