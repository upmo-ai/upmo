package domain

import (
	"bytes"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/url"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const (
	// MaxAvatarUploadBytes caps the raw uploaded image size at 50 MB.
	MaxAvatarUploadBytes = 50 << 20

	// MaxAvatarDimension caps decoded width and height at 8192 pixels.
	MaxAvatarDimension = 8192

	// MaxAvatarPixels caps total decoded pixel area at 40 megapixels.
	MaxAvatarPixels = 40_000_000

	// MaxAvatarOutputDim is the maximum width or height of the re-encoded PNG.
	MaxAvatarOutputDim = 512

	// AvatarThrottleBurst allows a short burst of avatar uploads or removals per user.
	AvatarThrottleBurst = 10

	// AvatarThrottlePerSecond refills 10 avatar writes per minute per user.
	AvatarThrottlePerSecond = 10.0 / 60.0
)

var (
	// ErrAvatarEmpty rejects an empty upload.
	ErrAvatarEmpty = errors.New("identity: avatar image is empty")
	// ErrAvatarTooLarge rejects uploads over MaxAvatarUploadBytes.
	ErrAvatarTooLarge = errors.New("identity: avatar exceeds 50 MB limit")
	// ErrAvatarUnsupportedMedia rejects anything outside the PNG/JPEG/GIF/WebP allowlist.
	ErrAvatarUnsupportedMedia = errors.New("identity: avatar must be a PNG, JPEG, GIF, or WebP image")
	// ErrAvatarDimensions rejects images exceeding the width, height, or pixel area caps.
	ErrAvatarDimensions = errors.New("identity: avatar dimensions are too large")
	// ErrAvatarCorrupt rejects malformed image payloads.
	ErrAvatarCorrupt = errors.New("identity: avatar image cannot be decoded")
	// ErrAvatarNotFound reports a missing or hidden user avatar.
	ErrAvatarNotFound = errors.New("identity: avatar not found")
)

// IsAllowedAvatarType reports whether a sniffed MIME type is in the avatar allowlist.
func IsAllowedAvatarType(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

// SniffAvatarType inspects magic bytes to identify an allowlisted image type.
// Non-allowlisted formats (including SVG, HTML, XML, BMP, and PDF) return "".
func SniffAvatarType(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case bytes.HasPrefix(raw, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case bytes.HasPrefix(raw, []byte("GIF87a")), bytes.HasPrefix(raw, []byte("GIF89a")):
		return "image/gif"
	case len(raw) >= 14 && bytes.Equal(raw[0:4], []byte("RIFF")) && bytes.Equal(raw[8:14], []byte("WEBPVP")):
		return "image/webp"
	default:
		return ""
	}
}

// NormalizeAvatar validates raw image bytes by magic bytes and header dimensions,
// decodes the image (first frame only for GIF), scales it down to fit within
// MaxAvatarOutputDim without upscaling, and re-encodes to a clean PNG with all
// metadata and trailing bytes stripped.
func NormalizeAvatar(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, ErrAvatarEmpty
	}
	if len(raw) > MaxAvatarUploadBytes {
		return nil, ErrAvatarTooLarge
	}

	mediaType := SniffAvatarType(raw)
	if !IsAllowedAvatarType(mediaType) {
		return nil, ErrAvatarUnsupportedMedia
	}

	cfg, err := decodeAvatarConfig(mediaType, raw)
	if err != nil {
		return nil, ErrAvatarCorrupt
	}
	if !validAvatarDimensions(cfg.Width, cfg.Height) {
		return nil, ErrAvatarDimensions
	}

	src, err := decodeAvatarImage(mediaType, raw)
	if err != nil {
		return nil, ErrAvatarCorrupt
	}
	srcBounds := src.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()
	if !validAvatarDimensions(srcW, srcH) {
		return nil, ErrAvatarDimensions
	}

	dstW, dstH := fitAvatarBounds(srcW, srcH, MaxAvatarOutputDim)
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	if dstW == srcW && dstH == srcH {
		draw.Copy(dst, image.Point{}, src, srcBounds, draw.Src, nil)
	} else {
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, srcBounds, draw.Src, nil)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, ErrAvatarCorrupt
	}
	return buf.Bytes(), nil
}

// AvatarURL formats the same-origin relative URL for a user's avatar, or nil
// when userID is zero or avatarKey is empty.
func AvatarURL(userID UserID, avatarKey string) *string {
	if userID == (UserID{}) || avatarKey == "" {
		return nil
	}
	u := "/api/users/" + userID.String() + "/avatar?v=" + url.QueryEscape(avatarKey)
	return &u
}

func validAvatarDimensions(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	if width > MaxAvatarDimension || height > MaxAvatarDimension {
		return false
	}
	return int64(width)*int64(height) <= MaxAvatarPixels
}

func decodeAvatarConfig(mediaType string, raw []byte) (image.Config, error) {
	r := bytes.NewReader(raw)
	switch mediaType {
	case "image/png":
		return png.DecodeConfig(r)
	case "image/jpeg":
		return jpeg.DecodeConfig(r)
	case "image/gif":
		return gif.DecodeConfig(r)
	case "image/webp":
		return webp.DecodeConfig(r)
	default:
		return image.Config{}, ErrAvatarUnsupportedMedia
	}
}

func decodeAvatarImage(mediaType string, raw []byte) (image.Image, error) {
	r := bytes.NewReader(raw)
	switch mediaType {
	case "image/png":
		return png.Decode(r)
	case "image/jpeg":
		return jpeg.Decode(r)
	case "image/gif":
		return gif.Decode(r)
	case "image/webp":
		return webp.Decode(r)
	default:
		return nil, ErrAvatarUnsupportedMedia
	}
}

func fitAvatarBounds(w, h, maxDim int) (int, int) {
	if w <= maxDim && h <= maxDim {
		return w, h
	}
	if w >= h {
		dstW := maxDim
		dstH := max(1, (h*maxDim+w/2)/w)
		return dstW, dstH
	}
	dstH := maxDim
	dstW := max(1, (w*maxDim+h/2)/h)
	return dstW, dstH
}
