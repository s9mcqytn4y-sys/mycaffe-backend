package imaging_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/imaging"
)

// testImage is blue with a red 30x30 block in the top-left corner.
func testImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{0, 0, 255, 255}
			if x < 30 && y < 30 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

// jpegWithOrientation encodes img and inserts an EXIF APP1 segment with the given orientation.
func jpegWithOrientation(t *testing.T, img image.Image, orientation uint16) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0} // little endian, IFD at 8, one entry
	entry := make([]byte, 12)
	binary.LittleEndian.PutUint16(entry[0:], 0x0112) // Orientation
	binary.LittleEndian.PutUint16(entry[2:], 3)      // SHORT
	binary.LittleEndian.PutUint32(entry[4:], 1)
	binary.LittleEndian.PutUint16(entry[8:], orientation)
	payload := append(append([]byte("Exif\x00\x00"), tiff...), append(entry, 0, 0, 0, 0)...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	data := buf.Bytes()
	return append(append(append([]byte{}, data[:2]...), append(seg, payload...)...), data[2:]...)
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xC000 && g < 0x4000 && b < 0x4000
}

func TestApplyEXIFOrientation(t *testing.T) {
	data := jpegWithOrientation(t, testImage(300, 200), 6) // rotate 90 degrees clockwise to display
	if !bytes.Contains(data, []byte("Exif")) {
		t.Fatal("test input must carry EXIF")
	}
	variants, err := imaging.Process(data, "image/jpeg", []int{1200, 100})
	if err != nil {
		t.Fatal(err)
	}
	full := variants[0]
	if full.Width != 200 || full.Height != 300 {
		t.Fatalf("orientation 6 must swap to 200x300 without upscaling, got %dx%d", full.Width, full.Height)
	}
	if variants[1].Width != 100 || variants[1].Height != 150 {
		t.Fatalf("100 px variant must keep the aspect ratio, got %dx%d", variants[1].Width, variants[1].Height)
	}
	if bytes.Contains(full.JPEG, []byte("Exif")) {
		t.Fatal("variants must not carry EXIF")
	}
	out, err := jpeg.Decode(bytes.NewReader(full.JPEG))
	if err != nil {
		t.Fatal(err)
	}
	// The red top-left block of the stored image is top-right once upright.
	if !isRed(out.At(190, 10)) || isRed(out.At(10, 10)) {
		t.Fatal("image was not rotated to its upright orientation")
	}
}

func TestProcessRejects(t *testing.T) {
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, testImage(300, 300))
	if _, err := imaging.Process(pngBuf.Bytes(), "image/jpeg", []int{200}); !errors.Is(err, imaging.ErrTypeMismatch) {
		t.Fatalf("PNG declared as JPEG must be ErrTypeMismatch, got %v", err)
	}
	if v, err := imaging.Process(pngBuf.Bytes(), "image/png", []int{200}); err != nil || v[0].Width != 200 {
		t.Fatalf("PNG declared as PNG must pass: %v", err)
	}
	small := jpegWithOrientation(t, testImage(100, 100), 1)
	if _, err := imaging.Process(small, "image/jpeg", []int{200}); !errors.Is(err, imaging.ErrDimensions) {
		t.Fatalf("100x100 must be ErrDimensions, got %v", err)
	}
	if _, err := imaging.Process([]byte("<html>not an image</html>"), "image/jpeg", []int{200}); !errors.Is(err, imaging.ErrUnsupportedType) {
		t.Fatalf("non-image must be ErrUnsupportedType, got %v", err)
	}
}
