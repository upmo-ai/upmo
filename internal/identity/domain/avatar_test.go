package domain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// minimalWebP is a valid 1x1 lossless WebP (VP8L) image (28 bytes).
var minimalWebP = []byte{
	'R', 'I', 'F', 'F', 0x14, 0x00, 0x00, 0x00,
	'W', 'E', 'B', 'P', 'V', 'P', '8', 'L',
	0x08, 0x00, 0x00, 0x00,
	0x2f, 0x00, 0x00, 0x00, 0x00, 0x88, 0x88, 0x08,
}

func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 0x20, G: 0x80, B: 0x60, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTestJPEGWithEXIFAndTrailer(t *testing.T, w, h int, exifCanary, trailer string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 0x10, G: 0x40, B: 0x90, A: 0xff})
		}
	}
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	b := raw.Bytes()
	payload := append([]byte("Exif\x00\x00"), []byte(exifCanary)...)
	segLen := len(payload) + 2
	if segLen > 0xffff {
		t.Fatalf("exif segment too large: %d", segLen)
	}
	var hdr [4]byte
	hdr[0] = 0xff
	hdr[1] = 0xe1
	binary.BigEndian.PutUint16(hdr[2:4], uint16(segLen)) //nolint:gosec // bounds checked above
	var out bytes.Buffer
	out.Write(b[:2]) // SOI
	out.Write(hdr[:])
	out.Write(payload)
	out.Write(b[2:])
	out.WriteString(trailer)
	return out.Bytes()
}

func makeTestPNGWithTextChunk(t *testing.T, w, h int, canary string) []byte {
	t.Helper()
	raw := makeTestPNG(t, w, h)
	// Insert a valid tEXt chunk right before the 12-byte IEND chunk.
	iendOffset := len(raw) - 12
	chunkData := append([]byte("Comment\x00"), []byte(canary)...)
	if len(chunkData) > 0x7fffffff {
		t.Fatalf("text chunk too large: %d", len(chunkData))
	}
	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.BigEndian, uint32(len(chunkData))) //nolint:gosec // bounds checked above
	chunk.WriteString("tEXt")
	chunk.Write(chunkData)
	crc := crc32.ChecksumIEEE(append([]byte("tEXt"), chunkData...))
	_ = binary.Write(&chunk, binary.BigEndian, crc)

	var out bytes.Buffer
	out.Write(raw[:iendOffset])
	out.Write(chunk.Bytes())
	out.Write(raw[iendOffset:])
	return out.Bytes()
}

func makeHeaderOnlyPNG(w, h uint32) []byte {
	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA
	_ = binary.Write(&out, binary.BigEndian, uint32(len(ihdr)))
	out.WriteString("IHDR")
	out.Write(ihdr[:])
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr[:]...)))
	// Empty IEND chunk.
	_ = binary.Write(&out, binary.BigEndian, uint32(0))
	out.WriteString("IEND")
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE([]byte("IEND")))
	return out.Bytes()
}

func makeAnimatedGIF(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	f1 := image.NewPaletted(image.Rect(0, 0, 16, 16), palette)
	f2 := image.NewPaletted(image.Rect(0, 0, 16, 16), palette)
	for i := range f2.Pix {
		f2.Pix[i] = 1
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{
		Image: []*image.Paletted{f1, f2},
		Delay: []int{10, 10},
	}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNormalizeAvatarFormatsAndResize(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
		wantW int
		wantH int
	}{
		{"small png no upscale", makeTestPNG(t, 64, 48), 64, 48},
		{"large jpeg downscale", makeTestJPEGWithEXIFAndTrailer(t, 1024, 768, "EXIF_GPS_SECRET_CANARY", "<script>alert(1)</script>"), 512, 384},
		{"tall png downscale", makeTestPNG(t, 400, 1000), 205, 512},
		{"animated gif collapsed", makeAnimatedGIF(t), 16, 16},
		{"webp decoded", minimalWebP, 1, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := NormalizeAvatar(tc.input)
			if err != nil {
				t.Fatalf("NormalizeAvatar: %v", err)
			}
			if SniffAvatarType(out) != "image/png" {
				t.Fatalf("output sniffed as %q, want image/png", SniffAvatarType(out))
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("decode output png: %v", err)
			}
			if cfg.Width != tc.wantW || cfg.Height != tc.wantH {
				t.Fatalf("output dims = %dx%d, want %dx%d", cfg.Width, cfg.Height, tc.wantW, tc.wantH)
			}
		})
	}
}

func TestNormalizeAvatarStripsMetadataAndPolyglots(t *testing.T) {
	jpg := makeTestJPEGWithEXIFAndTrailer(t, 1024, 768, "EXIF_GPS_SECRET_CANARY", "<script>alert(1)</script>")
	outJPG, err := NormalizeAvatar(jpg)
	if err != nil {
		t.Fatalf("jpeg normalize: %v", err)
	}
	for _, needle := range []string{"EXIF_GPS_SECRET_CANARY", "<script>", "Exif"} {
		if bytes.Contains(outJPG, []byte(needle)) {
			t.Fatalf("output PNG retained %q", needle)
		}
	}

	pngWithText := makeTestPNGWithTextChunk(t, 32, 32, "PNG_TEXT_CANARY")
	if !bytes.Contains(pngWithText, []byte("PNG_TEXT_CANARY")) {
		t.Fatal("test fixture missing PNG_TEXT_CANARY")
	}
	outPNG, err := NormalizeAvatar(pngWithText)
	if err != nil {
		t.Fatalf("png normalize: %v", err)
	}
	if bytes.Contains(outPNG, []byte("PNG_TEXT_CANARY")) {
		t.Fatal("output PNG retained PNG_TEXT_CANARY")
	}
}

func TestNormalizeAvatarRejectsDisallowedAndInvalidInputs(t *testing.T) {
	if _, err := NormalizeAvatar(nil); !errors.Is(err, ErrAvatarEmpty) {
		t.Fatalf("empty err = %v, want ErrAvatarEmpty", err)
	}

	for name, payload := range map[string][]byte{
		"svg bare": []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		"svg xml":  []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html":     []byte(`<!DOCTYPE html><html><body><script>alert(1)</script></body></html>`),
		"pdf":      []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n"),
		"bmp":      {'B', 'M', 0, 0, 0, 0, 0, 0, 0, 0},
		"plain":    []byte("not an image"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeAvatar(payload); !errors.Is(err, ErrAvatarUnsupportedMedia) {
				t.Fatalf("%s err = %v, want ErrAvatarUnsupportedMedia", name, err)
			}
		})
	}

	t.Run("dimension cap width", func(t *testing.T) {
		if _, err := NormalizeAvatar(makeHeaderOnlyPNG(8193, 100)); !errors.Is(err, ErrAvatarDimensions) {
			t.Fatalf("err = %v, want ErrAvatarDimensions", err)
		}
	})
	t.Run("dimension cap area", func(t *testing.T) {
		if _, err := NormalizeAvatar(makeHeaderOnlyPNG(6400, 6400)); !errors.Is(err, ErrAvatarDimensions) {
			t.Fatalf("err = %v, want ErrAvatarDimensions", err)
		}
	})
	t.Run("corrupt png", func(t *testing.T) {
		corrupt := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("garbage")...)
		if _, err := NormalizeAvatar(corrupt); !errors.Is(err, ErrAvatarCorrupt) {
			t.Fatalf("err = %v, want ErrAvatarCorrupt", err)
		}
	})
}

func TestAvatarURL(t *testing.T) {
	id, err := ParseUserID("018f3b2c-7a10-7c4a-8e11-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if got := AvatarURL(UserID{}, "key"); got != nil {
		t.Fatalf("zero user = %q, want nil", *got)
	}
	if got := AvatarURL(id, ""); got != nil {
		t.Fatalf("empty key = %q, want nil", *got)
	}
	got := AvatarURL(id, "018f3b2c-7a10-7c4a-8e11-222222222222")
	want := "/api/users/018f3b2c-7a10-7c4a-8e11-111111111111/avatar?v=018f3b2c-7a10-7c4a-8e11-222222222222"
	if got == nil || *got != want {
		t.Fatalf("AvatarURL = %v, want %q", got, want)
	}
}
