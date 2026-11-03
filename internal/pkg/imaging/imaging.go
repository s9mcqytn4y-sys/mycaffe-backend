// Package imaging verifies uploaded product photos and renders resized JPEG variants. Decoding and
// re-encoding drops every metadata block (EXIF, GPS, ICC, comments); the EXIF orientation of a JPEG
// is applied first so phone photos stay upright.
package imaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const (
	MinSide     = 200
	MaxPixels   = 25_000_000
	jpegQuality = 80
)

var (
	ErrUnsupportedType = errors.New("unsupported image type")
	ErrTypeMismatch    = errors.New("image content does not match the declared type")
	ErrDimensions      = errors.New("image dimensions out of range")
)

// Variant is one rendered size.
type Variant struct {
	Width  int
	Height int
	JPEG   []byte
}

// Sniff returns the MIME type detected from the content (image/jpeg, image/png or image/webp).
func Sniff(data []byte) (string, error) {
	switch t := http.DetectContentType(data); t {
	case "image/jpeg", "image/png", "image/webp":
		return t, nil
	default:
		return "", ErrUnsupportedType
	}
}

// Process checks that data is a declaredType image within the size limits and renders one JPEG per
// requested width (never upscaled).
func Process(data []byte, declaredType string, widths []int) ([]Variant, error) {
	actual, err := Sniff(data)
	if err != nil {
		return nil, err
	}
	if actual != declaredType {
		return nil, ErrTypeMismatch
	}
	cfg, err := decodeConfig(data, actual)
	if err != nil {
		return nil, ErrUnsupportedType
	}
	if cfg.Width < MinSide || cfg.Height < MinSide || cfg.Width*cfg.Height > MaxPixels {
		return nil, ErrDimensions
	}
	src, err := decode(data, actual)
	if err != nil {
		return nil, ErrUnsupportedType
	}
	if actual == "image/jpeg" {
		src = applyOrientation(src, jpegOrientation(data))
	}

	variants := make([]Variant, 0, len(widths))
	for _, w := range widths {
		b := src.Bounds()
		if w > b.Dx() {
			w = b.Dx()
		}
		h := b.Dy() * w / b.Dx()
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		// White background so transparent PNG/WebP areas do not turn black in JPEG.
		draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return nil, err
		}
		variants = append(variants, Variant{Width: w, Height: h, JPEG: buf.Bytes()})
	}
	return variants, nil
}

func decodeConfig(data []byte, typ string) (image.Config, error) {
	r := bytes.NewReader(data)
	switch typ {
	case "image/jpeg":
		return jpeg.DecodeConfig(r)
	case "image/png":
		return png.DecodeConfig(r)
	default:
		return webp.DecodeConfig(r)
	}
}

func decode(data []byte, typ string) (image.Image, error) {
	r := bytes.NewReader(data)
	switch typ {
	case "image/jpeg":
		return jpeg.Decode(r)
	case "image/png":
		return png.Decode(r)
	default:
		return webp.Decode(r)
	}
}

// jpegOrientation returns the EXIF orientation (1-8) of a JPEG, or 1 when absent or unreadable.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1
		}
		marker, size := data[i+1], int(binary.BigEndian.Uint16(data[i+2:]))
		if marker == 0xDA || size < 2 || i+2+size > len(data) { // start of scan: no more metadata
			return 1
		}
		seg := data[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[ifd:]))
	for e := 0; e < n; e++ {
		off := ifd + 2 + e*12
		if off+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[off:]) == 0x0112 {
			if v := int(bo.Uint16(t[off+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// applyOrientation turns an EXIF-oriented image upright (orientations 2-8 per the EXIF spec).
func applyOrientation(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
