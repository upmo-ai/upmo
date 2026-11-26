//go:build integration

package identity_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

// minimalAvatarWebP is a valid 1x1 lossless WebP (VP8L) image (28 bytes).
var minimalAvatarWebP = []byte{
	'R', 'I', 'F', 'F', 0x14, 0x00, 0x00, 0x00,
	'W', 'E', 'B', 'P', 'V', 'P', '8', 'L',
	0x08, 0x00, 0x00, 0x00,
	0x2f, 0x00, 0x00, 0x00, 0x00, 0x88, 0x88, 0x08,
}

type avatarRig struct {
	owner      *pgxpool.Pool
	pool       *pgxpool.Pool
	ctx        context.Context
	avatarsDir string
	handler    http.Handler
	sessions   identity.SessionService
	members    identity.MemberService
	tokens     identity.TokenService
	prov       identity.ProvisionStore
	emails     identity.EmailSealer
	now        time.Time
}

func newAvatarRig(t *testing.T) *avatarRig {
	t.Helper()
	owner, pool, ctx := upBoth(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	avatarsDir := t.TempDir()
	emails := sealer.NewEmail(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	sessions := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	sessions.Now = func() time.Time { return now }
	tokens := identity.APITokens(pool, log)
	members := identity.WithAvatars(identity.Members(pool, emails, "https://upmo.test", nil, log), avatarsDir)
	members.Now = func() time.Time { return now }
	prov := identity.WithProvisionAvatars(identity.Provision(owner, emails, "https://upmo.test"), avatarsDir)
	prov.Now = func() time.Time { return now }

	mux := http.NewServeMux()
	identity.MountAPITokens(mux, sessions, tokens, log, nil, pool, nil)
	identity.MountMembers(mux, members, sessions)
	(httpapi.Avatars{
		Members:  members,
		Sessions: sessions,
		Limit:    postgres.Passkeys{Pool: pool},
		Now:      func() time.Time { return now },
	}).Mount(mux)

	served := httpserver.SecurityHeaders(
		audit.Middleware(audit.Routes(), mux, pool, "brain", nil, log)(mux),
	)

	return &avatarRig{
		owner:      owner,
		pool:       pool,
		ctx:        ctx,
		avatarsDir: avatarsDir,
		handler:    served,
		sessions:   sessions,
		members:    members,
		tokens:     tokens,
		prov:       prov,
		emails:     emails,
		now:        now,
	}
}

func (h *avatarRig) seedUser(t *testing.T, email, name, roleKey string) (domain.UserID, string, identity.Session) {
	t.Helper()
	stored, hash, keyID, err := h.emails.Seal(t.Context(), email)
	if err != nil {
		t.Fatal(err)
	}
	var idText string
	if err := h.owner.QueryRow(h.ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at, joined_at)
		VALUES ($1, $2, $3, $4, (SELECT id FROM identity.roles WHERE key = $5), $6, $6)
		RETURNING id::text`, stored, hash, keyID, name, roleKey, h.now).Scan(&idText); err != nil {
		t.Fatal(err)
	}
	uid, err := domain.ParseUserID(idText)
	if err != nil {
		t.Fatal(err)
	}
	cookie, sess, err := h.sessions.Start(h.ctx, uid, "passkey", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	return uid, cookie, sess
}

func (h *avatarRig) avatarKeyOf(t *testing.T, uid domain.UserID) string {
	t.Helper()
	var key string
	if err := h.owner.QueryRow(h.ctx, `SELECT COALESCE(avatar_key, '') FROM identity.users WHERE id = $1`, uid).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

func (h *avatarRig) putMultipart(t *testing.T, path, cookie, bearer, fieldName, fileName, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, fieldName, fileName))
	if contentType != "" {
		hdr.Set("Content-Type", contentType)
	}
	part, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPut, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: cookie})
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *avatarRig) doRequest(method, path, cookie, bearer string, headers map[string]string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: cookie})
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func buildPNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildJPEGWithEXIFAndTrailer(t *testing.T, w, h int, exifCanary, trailer string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 0x33, G: 0x66, B: 0x99, A: 0xff})
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
	out.Write(b[:2])
	out.Write(hdr[:])
	out.Write(payload)
	out.Write(b[2:])
	out.WriteString(trailer)
	return out.Bytes()
}

func buildPNGWithTextChunk(t *testing.T, w, h int, canary string) []byte {
	t.Helper()
	raw := buildPNG(t, w, h, color.NRGBA{R: 0x20, G: 0x50, B: 0x70, A: 0xff})
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

func buildAnimatedGIF(t *testing.T) []byte {
	t.Helper()
	pal := color.Palette{color.RGBA{R: 200, G: 50, B: 50, A: 255}, color.RGBA{R: 50, G: 50, B: 200, A: 255}}
	f1 := image.NewPaletted(image.Rect(0, 0, 20, 20), pal)
	f2 := image.NewPaletted(image.Rect(0, 0, 20, 20), pal)
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

func buildHeaderOnlyPNG(w, h uint32) []byte {
	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8
	ihdr[9] = 6
	_ = binary.Write(&out, binary.BigEndian, uint32(len(ihdr)))
	out.WriteString("IHDR")
	out.Write(ihdr[:])
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr[:]...)))
	_ = binary.Write(&out, binary.BigEndian, uint32(0))
	out.WriteString("IEND")
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE([]byte("IEND")))
	return out.Bytes()
}

// zeroStreamReader streams rem zero bytes without allocating the whole buffer.
type zeroStreamReader struct {
	rem int64
}

func (r *zeroStreamReader) Read(p []byte) (int, error) {
	if r.rem <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.rem {
		n = r.rem
	}
	clear(p[:n])
	r.rem -= n
	return int(n), nil
}

func parseAvatarURLResponse(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse avatar_url response %q: %v", rec.Body.String(), err)
	}
	if body.AvatarURL == "" {
		t.Fatalf("empty avatar_url in %q", rec.Body.String())
	}
	return body.AvatarURL
}

func keyFromAvatarURL(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse avatar url %q: %v", rawURL, err)
	}
	v := u.Query().Get("v")
	if v == "" {
		t.Fatalf("missing ?v= in %q", rawURL)
	}
	return v
}

func TestAvatarConcurrentUploadLimit(t *testing.T) {
	h := newAvatarRig(t)
	_, cookieA, _ := h.seedUser(t, "ada@example.com", "Ada", "owner")
	_, cookieB, _ := h.seedUser(t, "bob@example.com", "Bob", "member")
	body, contentType := avatarMultipartBody(t)
	slots := make(chan struct{}, 1)
	mux := http.NewServeMux()
	(httpapi.Avatars{Members: h.members, Sessions: h.sessions, UploadSlots: slots}).Mount(mux)

	firstStarted := make(chan struct{})
	continueFirst := make(chan struct{})
	firstBody := &gatedReader{body: bytes.NewReader(body), started: firstStarted, proceed: continueFirst}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		req := httptest.NewRequest(http.MethodPut, "/api/me/avatar", firstBody)
		req.Header.Set("Content-Type", contentType)
		req.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: cookieA})
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-firstStarted

	secondRead := make(chan struct{}, 1)
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		req := httptest.NewRequest(http.MethodPut, "/api/me/avatar", &observedReader{reader: bytes.NewReader(body), read: secondRead})
		req.Header.Set("Content-Type", contentType)
		req.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: cookieB})
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}()

	select {
	case <-secondRead:
		t.Fatal("second upload read its body while the first held the only slot")
	case <-time.After(100 * time.Millisecond):
	}
	close(continueFirst)
	<-firstDone
	select {
	case <-secondRead:
	case <-time.After(5 * time.Second):
		t.Fatal("second upload did not start reading after the first finished")
	}
	<-secondDone
}

type gatedReader struct {
	body    *bytes.Reader
	started chan struct{}
	proceed chan struct{}
	once    bool
}

func (r *gatedReader) Read(p []byte) (int, error) {
	if !r.once {
		r.once = true
		close(r.started)
		<-r.proceed
	}
	return r.body.Read(p)
}

type observedReader struct {
	reader io.Reader
	read   chan struct{}
	once   atomic.Bool
}

func (r *observedReader) Read(p []byte) (int, error) {
	if r.once.CompareAndSwap(false, true) {
		r.read <- struct{}{}
	}
	return r.reader.Read(p)
}

func avatarMultipartBody(t *testing.T) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("avatar", "avatar.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(buildPNG(t, 8, 8, color.NRGBA{R: 1, G: 2, B: 3, A: 255})); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), mw.FormDataContentType()
}

func TestAvatarUploadSizeCap(t *testing.T) {
	h := newAvatarRig(t)
	uid, cookie, _ := h.seedUser(t, "ada@example.com", "Ada", "owner")

	// Valid image under 50 MB succeeds.
	okRec := h.putMultipart(t, "/api/me/avatar", cookie, "", "avatar", "photo.png", "image/png", buildPNG(t, 32, 32, color.NRGBA{R: 10, G: 20, B: 30, A: 255}))
	if okRec.Code != http.StatusOK {
		t.Fatalf("valid upload status = %d body = %s", okRec.Code, okRec.Body.String())
	}
	beforeKey := h.avatarKeyOf(t, uid)
	if beforeKey == "" {
		t.Fatal("avatar_key not saved")
	}

	// Stream a multipart payload with file size = 50 MB + 1 byte without holding 50 MB in memory.
	const maxAvatarBytes = 50 << 20
	const boundary = "upmoAvatarLimitBoundary"
	prefix := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"avatar\"; filename=\"huge.png\"\r\nContent-Type: image/png\r\n\r\n", boundary)
	pngMagic := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	suffix := fmt.Sprintf("\r\n--%s--\r\n", boundary)
	streamBody := io.MultiReader(
		strings.NewReader(prefix),
		bytes.NewReader(pngMagic),
		&zeroStreamReader{rem: maxAvatarBytes + 1 - int64(len(pngMagic))},
		strings.NewReader(suffix),
	)
	rec := h.doRequest(http.MethodPut, "/api/me/avatar", cookie, "", map[string]string{
		"Content-Type": "multipart/form-data; boundary=" + boundary,
	}, streamBody)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-50MB upload status = %d, want 413, body = %s", rec.Code, rec.Body.String())
	}
	if got := h.avatarKeyOf(t, uid); got != beforeKey {
		t.Fatalf("avatar_key changed on 413: got %q, want %q", got, beforeKey)
	}

	// Upload format rules (Decision #5): raw body, wrong field name, missing part, extra parts all return 400.
	t.Run("rejects raw body", func(t *testing.T) {
		r := h.doRequest(http.MethodPut, "/api/me/avatar", cookie, "", map[string]string{
			"Content-Type": "image/png",
		}, bytes.NewReader(buildPNG(t, 8, 8, color.NRGBA{A: 255})))
		if r.Code != http.StatusBadRequest {
			t.Fatalf("raw body status = %d, want 400, body = %s", r.Code, r.Body.String())
		}
	})
	t.Run("rejects wrong field name", func(t *testing.T) {
		r := h.putMultipart(t, "/api/me/avatar", cookie, "", "file", "photo.png", "image/png", buildPNG(t, 8, 8, color.NRGBA{A: 255}))
		if r.Code != http.StatusBadRequest {
			t.Fatalf("wrong field status = %d, want 400, body = %s", r.Code, r.Body.String())
		}
	})
	t.Run("rejects extra parts", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		p, _ := mw.CreateFormFile("avatar", "photo.png")
		_, _ = p.Write(buildPNG(t, 8, 8, color.NRGBA{A: 255}))
		_ = mw.WriteField("extra", "nope")
		_ = mw.Close()
		r := h.doRequest(http.MethodPut, "/api/me/avatar", cookie, "", map[string]string{
			"Content-Type": mw.FormDataContentType(),
		}, &buf)
		if r.Code != http.StatusBadRequest {
			t.Fatalf("extra parts status = %d, want 400, body = %s", r.Code, r.Body.String())
		}
	})
	t.Run("rejects empty multipart", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.Close()
		r := h.doRequest(http.MethodPut, "/api/me/avatar", cookie, "", map[string]string{
			"Content-Type": mw.FormDataContentType(),
		}, &buf)
		if r.Code != http.StatusBadRequest {
			t.Fatalf("empty multipart status = %d, want 400, body = %s", r.Code, r.Body.String())
		}
	})
}

func TestAvatarSniffedTypeAllowlist(t *testing.T) {
	h := newAvatarRig(t)
	_, cookie, _ := h.seedUser(t, "ada@example.com", "Ada", "owner")

	// Valid PNG, JPEG, 2-frame animated GIF, and WebP succeed even with misleading client Content-Type and filename.
	for name, payload := range map[string][]byte{
		"png":  buildPNG(t, 16, 16, color.NRGBA{R: 100, G: 20, B: 30, A: 255}),
		"jpeg": buildJPEGWithEXIFAndTrailer(t, 16, 16, "x", ""),
		"gif":  buildAnimatedGIF(t),
		"webp": minimalAvatarWebP,
	} {
		t.Run("allow "+name, func(t *testing.T) {
			rec := h.putMultipart(t, "/api/me/avatar", cookie, "", "avatar", "blob.bin", "application/octet-stream", payload)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s upload status = %d, want 200, body = %s", name, rec.Code, rec.Body.String())
			}
			avatarURL := parseAvatarURLResponse(t, rec)
			getRec := h.doRequest(http.MethodGet, avatarURL, cookie, "", nil, nil)
			if getRec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d", avatarURL, getRec.Code)
			}
			if _, err := png.Decode(bytes.NewReader(getRec.Body.Bytes())); err != nil {
				t.Fatalf("served %s is not valid PNG: %v", name, err)
			}
		})
	}

	// Non-image payloads disguised as image/png are rejected with 415.
	for name, payload := range map[string][]byte{
		"pdf":    []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n"),
		"text":   []byte("hello world plain text"),
		"bmp":    {'B', 'M', 0x36, 0, 0, 0, 0, 0, 0, 0, 0x36, 0, 0, 0},
		"random": {0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
	} {
		t.Run("reject "+name, func(t *testing.T) {
			rec := h.putMultipart(t, "/api/me/avatar", cookie, "", "avatar", "photo.png", "image/png", payload)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("%s status = %d, want 415, body = %s", name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAvatarRejectsSVG(t *testing.T) {
	h := newAvatarRig(t)
	_, cookie, _ := h.seedUser(t, "ada@example.com", "Ada", "owner")

	for name, svg := range map[string][]byte{
		"bare svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		"xml svg":  []byte(`<?xml version="1.0" encoding="UTF-8"?><svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
	} {
		t.Run(name, func(t *testing.T) {
			rec := h.putMultipart(t, "/api/me/avatar", cookie, "", "avatar", "avatar.png", "image/png", svg)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("%s status = %d, want 415, body = %s", name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAvatarPixelCap(t *testing.T) {
	h := newAvatarRig(t)
	_, cookie, _ := h.seedUser(t, "ada@example.com", "Ada", "owner")

	for name, raw := range map[string][]byte{
		"width over 8192":  buildHeaderOnlyPNG(8193, 100),
		"area over 40 MP":  buildHeaderOnlyPNG(6400, 6400),
		"height over 8192": buildHeaderOnlyPNG(100, 8193),
	} {
		t.Run(name, func(t *testing.T) {
			rec := h.putMultipart(t, "/api/me/avatar", cookie, "", "avatar", "bomb.png", "image/png", raw)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s status = %d, want 422, body = %s", name, rec.Code, rec.Body.String())
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s decode error json: %v", name, err)
			}
			if body.Error != "image dimensions are too large" {
				t.Fatalf("%s error = %q, want %q", name, body.Error, "image dimensions are too large")
			}
		})
	}
}

func TestAvatarStripsMetadataAndResizes(t *testing.T) {
	h := newAvatarRig(t)
	_, cookie, _ := h.seedUser(t, "ada@example.com", "Ada", "owner")

	jpg := buildJPEGWithEXIFAndTrailer(t, 1024, 768, "EXIF_GPS_SECRET_CANARY", "<script>alert(1)</script>")
	rec := h.putMultipart(t, "/api/me/avatar", cookie, "", "avatar", "camera.jpg", "image/jpeg", jpg)
	if rec.Code != http.StatusOK {
		t.Fatalf("jpeg upload status = %d body = %s", rec.Code, rec.Body.String())
	}
	avatarURL := parseAvatarURLResponse(t, rec)
	getRec := h.doRequest(http.MethodGet, avatarURL, cookie, "", nil, nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get jpeg avatar status = %d", getRec.Code)
	}
	body := getRec.Body.Bytes()
	cfg, err := png.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode served avatar as png: %v", err)
	}
	if cfg.Width != 512 || cfg.Height != 384 {
		t.Fatalf("resized dims = %dx%d, want 512x384", cfg.Width, cfg.Height)
	}
	for _, needle := range []string{"EXIF_GPS_SECRET_CANARY", "<script>"} {
		if bytes.Contains(body, []byte(needle)) {
			t.Fatalf("served avatar still contains %q", needle)
		}
	}

	pngWithText := buildPNGWithTextChunk(t, 64, 64, "PNG_TEXT_CANARY")
	rec2 := h.putMultipart(t, "/api/me/avatar", cookie, "", "avatar", "text.png", "image/png", pngWithText)
	if rec2.Code != http.StatusOK {
		t.Fatalf("png upload status = %d body = %s", rec2.Code, rec2.Body.String())
	}
	getRec2 := h.doRequest(http.MethodGet, parseAvatarURLResponse(t, rec2), cookie, "", nil, nil)
	if getRec2.Code != http.StatusOK {
		t.Fatalf("get png avatar status = %d", getRec2.Code)
	}
	if bytes.Contains(getRec2.Body.Bytes(), []byte("PNG_TEXT_CANARY")) {
		t.Fatal("served PNG avatar still contains PNG_TEXT_CANARY")
	}
}

func TestAvatarSelfOnlyAndSessionRequired(t *testing.T) {
	h := newAvatarRig(t)
	userA, cookieA, sessA := h.seedUser(t, "ada@example.com", "Ada", "owner")
	userB, _, _ := h.seedUser(t, "bob@example.com", "Bob", "member")
	pngBytes := buildPNG(t, 16, 16, color.NRGBA{R: 50, G: 100, B: 150, A: 255})

	// User A uploading (even with a forged user_id query param) updates only User A, never User B.
	rec := h.putMultipart(t, "/api/me/avatar?user_id="+userB.String(), cookieA, "", "avatar", "a.png", "image/png", pngBytes)
	if rec.Code != http.StatusOK {
		t.Fatalf("user A upload status = %d body = %s", rec.Code, rec.Body.String())
	}
	if h.avatarKeyOf(t, userA) == "" {
		t.Fatal("user A avatar_key was not set")
	}
	if got := h.avatarKeyOf(t, userB); got != "" {
		t.Fatalf("user B avatar_key = %q, want empty", got)
	}

	// Unauthenticated PUT and DELETE return 401.
	if r := h.putMultipart(t, "/api/me/avatar", "", "", "avatar", "a.png", "image/png", pngBytes); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauth PUT status = %d, want 401", r.Code)
	}
	if r := h.doRequest(http.MethodDelete, "/api/me/avatar", "", "", nil, nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauth DELETE status = %d, want 401", r.Code)
	}

	// Bearer API token PUT and DELETE return 403.
	_, secret, err := h.tokens.Create(h.ctx, sessA, "avatar-bot", []string{"task:view"}, 30, "")
	if err != nil {
		t.Fatal(err)
	}
	if r := h.putMultipart(t, "/api/me/avatar", "", secret, "avatar", "a.png", "image/png", pngBytes); r.Code != http.StatusForbidden {
		t.Fatalf("bearer PUT status = %d, want 403", r.Code)
	}
	if r := h.doRequest(http.MethodDelete, "/api/me/avatar", "", secret, nil, nil); r.Code != http.StatusForbidden {
		t.Fatalf("bearer DELETE status = %d, want 403", r.Code)
	}

	// upmo_app cannot directly UPDATE identity.users.avatar_key (SQLSTATE 42501).
	_, err = h.pool.Exec(h.ctx, `UPDATE identity.users SET avatar_key = '018f3b2c-7a10-7c4a-8e11-111111111111' WHERE id = $1`, userB)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("direct UPDATE avatar_key err = %v, want SQLSTATE 42501", err)
	}

	// identity.avatar_set rejects unknown/nil session token hashes with 42501 and bad keys with 22023.
	if _, err := h.pool.Exec(h.ctx, `SELECT identity.avatar_set($1, $2)`, []byte{9, 9, 9}, "018f3b2c-7a10-7c4a-8e11-111111111111"); !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("avatar_set bad session err = %v, want 42501", err)
	}
	if _, err := h.pool.Exec(h.ctx, `SELECT identity.avatar_set($1, $2)`, sessA.TokenHash, "../escape"); !errors.As(err, &pgErr) || pgErr.Code != "22023" {
		t.Fatalf("avatar_set bad key err = %v, want 22023", err)
	}
}

func TestAvatarReadAuth(t *testing.T) {
	h := newAvatarRig(t)
	userA, cookieA, sessA := h.seedUser(t, "ada@example.com", "Ada", "owner")
	_, cookieB, _ := h.seedUser(t, "bob@example.com", "Bob", "member")

	rec := h.putMultipart(t, "/api/me/avatar", cookieA, "", "avatar", "a.png", "image/png", buildPNG(t, 16, 16, color.NRGBA{R: 10, G: 200, B: 10, A: 255}))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d", rec.Code)
	}
	avatarURL := parseAvatarURLResponse(t, rec)

	// Unauthenticated GET -> 401.
	if r := h.doRequest(http.MethodGet, avatarURL, "", "", nil, nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauth GET status = %d, want 401", r.Code)
	}

	// Bearer token GET -> 403.
	_, secret, err := h.tokens.Create(h.ctx, sessA, "reader-bot", []string{"task:view"}, 30, "")
	if err != nil {
		t.Fatal(err)
	}
	if r := h.doRequest(http.MethodGet, avatarURL, "", secret, nil, nil); r.Code != http.StatusForbidden {
		t.Fatalf("bearer GET status = %d, want 403", r.Code)
	}

	// Another active workspace member (User B) can read User A's avatar -> 200 OK.
	got := h.doRequest(http.MethodGet, avatarURL, cookieB, "", nil, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("member B GET status = %d, want 200", got.Code)
	}
	if _, err := png.Decode(bytes.NewReader(got.Body.Bytes())); err != nil {
		t.Fatalf("member B did not receive valid PNG: %v", err)
	}

	// Invalid UUID or user without avatar -> 404.
	if r := h.doRequest(http.MethodGet, "/api/users/not-a-uuid/avatar", cookieB, "", nil, nil); r.Code != http.StatusNotFound {
		t.Fatalf("invalid uuid GET status = %d, want 404", r.Code)
	}
	if r := h.doRequest(http.MethodGet, "/api/users/"+userA.String()+"/avatar", cookieB, "", nil, nil); r.Code != http.StatusOK {
		t.Fatalf("unversioned GET status = %d, want 200", r.Code)
	}
}

func TestAvatarServingHeaders(t *testing.T) {
	h := newAvatarRig(t)
	userA, cookieA, _ := h.seedUser(t, "ada@example.com", "Ada", "owner")

	img1 := buildPNG(t, 24, 24, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
	up1 := h.putMultipart(t, "/api/me/avatar", cookieA, "", "avatar", "first.png", "image/png", img1)
	if up1.Code != http.StatusOK {
		t.Fatalf("upload 1 status = %d", up1.Code)
	}
	url1 := parseAvatarURLResponse(t, up1)
	key1 := keyFromAvatarURL(t, url1)

	get1 := h.doRequest(http.MethodGet, url1, cookieA, "", nil, nil)
	if get1.Code != http.StatusOK {
		t.Fatalf("GET 1 status = %d", get1.Code)
	}
	wantHeaders := map[string]string{
		"Content-Type":            "image/png",
		"X-Content-Type-Options":  "nosniff",
		"Content-Disposition":     `inline; filename="avatar.png"`,
		"Content-Security-Policy": "default-src 'none'; sandbox",
		"ETag":                    `"` + key1 + `"`,
		"Cache-Control":           "private, max-age=31536000, immutable",
	}
	for k, want := range wantHeaders {
		if got := get1.Header().Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}

	// Unversioned request gets private, no-cache.
	unver := h.doRequest(http.MethodGet, "/api/users/"+userA.String()+"/avatar", cookieA, "", nil, nil)
	if unver.Code != http.StatusOK || unver.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("unversioned status=%d Cache-Control=%q", unver.Code, unver.Header().Get("Cache-Control"))
	}

	// If-None-Match returns 304 Not Modified with empty body.
	notMod := h.doRequest(http.MethodGet, url1, cookieA, "", map[string]string{
		"If-None-Match": `"` + key1 + `"`,
	}, nil)
	if notMod.Code != http.StatusNotModified || notMod.Body.Len() != 0 {
		t.Fatalf("If-None-Match status=%d bodyLen=%d, want 304 empty", notMod.Code, notMod.Body.Len())
	}

	// GET /api/session and GET /api/members carry the same-origin avatar_url.
	sessRec := h.doRequest(http.MethodGet, "/api/session", cookieA, "", nil, nil)
	var sessPayload struct {
		AvatarURL *string `json:"avatar_url"`
	}
	if err := json.Unmarshal(sessRec.Body.Bytes(), &sessPayload); err != nil {
		t.Fatal(err)
	}
	if sessPayload.AvatarURL == nil || *sessPayload.AvatarURL != url1 {
		t.Fatalf("/api/session avatar_url = %v, want %q", sessPayload.AvatarURL, url1)
	}

	membersRec := h.doRequest(http.MethodGet, "/api/members", cookieA, "", nil, nil)
	var membersPayload []struct {
		ID        string  `json:"id"`
		AvatarURL *string `json:"avatar_url"`
	}
	if err := json.Unmarshal(membersRec.Body.Bytes(), &membersPayload); err != nil {
		t.Fatal(err)
	}
	if len(membersPayload) != 1 || membersPayload[0].AvatarURL == nil || *membersPayload[0].AvatarURL != url1 {
		t.Fatalf("/api/members avatar_url = %+v, want %q", membersPayload, url1)
	}

	// Replacement upload produces a new ?v= URL and immediately serves the new image bytes.
	img2 := buildPNG(t, 24, 24, color.NRGBA{R: 0, G: 0, B: 255, A: 255})
	up2 := h.putMultipart(t, "/api/me/avatar", cookieA, "", "avatar", "second.png", "image/png", img2)
	if up2.Code != http.StatusOK {
		t.Fatalf("upload 2 status = %d", up2.Code)
	}
	url2 := parseAvatarURLResponse(t, up2)
	if url2 == url1 {
		t.Fatalf("replacement avatar_url did not change: %q", url2)
	}
	get2 := h.doRequest(http.MethodGet, url2, cookieA, "", nil, nil)
	if get2.Code != http.StatusOK || bytes.Equal(get2.Body.Bytes(), get1.Body.Bytes()) {
		t.Fatalf("replacement GET status=%d identical=%t", get2.Code, bytes.Equal(get2.Body.Bytes(), get1.Body.Bytes()))
	}
}

func TestAvatarOldObjectDeleted(t *testing.T) {
	h := newAvatarRig(t)
	userA, cookieA, _ := h.seedUser(t, "ada@example.com", "Ada", "owner")

	up1 := h.putMultipart(t, "/api/me/avatar", cookieA, "", "avatar", "1.png", "image/png", buildPNG(t, 12, 12, color.NRGBA{R: 1, A: 255}))
	if up1.Code != http.StatusOK {
		t.Fatalf("upload 1 status = %d", up1.Code)
	}
	key1 := keyFromAvatarURL(t, parseAvatarURLResponse(t, up1))
	if _, err := os.Stat(filepath.Join(h.avatarsDir, key1)); err != nil {
		t.Fatalf("key1 missing on disk: %v", err)
	}

	up2 := h.putMultipart(t, "/api/me/avatar", cookieA, "", "avatar", "2.png", "image/png", buildPNG(t, 12, 12, color.NRGBA{R: 2, A: 255}))
	if up2.Code != http.StatusOK {
		t.Fatalf("upload 2 status = %d", up2.Code)
	}
	key2 := keyFromAvatarURL(t, parseAvatarURLResponse(t, up2))
	if _, err := os.Stat(filepath.Join(h.avatarsDir, key2)); err != nil {
		t.Fatalf("key2 missing on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.avatarsDir, key1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old key1 survived replace: %v", err)
	}

	del := h.doRequest(http.MethodDelete, "/api/me/avatar", cookieA, "", nil, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", del.Code)
	}
	if _, err := os.Stat(filepath.Join(h.avatarsDir, key2)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("key2 survived DELETE: %v", err)
	}
	if r := h.doRequest(http.MethodGet, "/api/users/"+userA.String()+"/avatar", cookieA, "", nil, nil); r.Code != http.StatusNotFound {
		t.Fatalf("GET after DELETE status = %d, want 404", r.Code)
	}
	// Second DELETE is idempotent.
	if del2 := h.doRequest(http.MethodDelete, "/api/me/avatar", cookieA, "", nil, nil); del2.Code != http.StatusNoContent {
		t.Fatalf("second DELETE status = %d, want 204", del2.Code)
	}
}

func TestAvatarRemovedOnUserErasure(t *testing.T) {
	h := newAvatarRig(t)
	_, ownerCookie, _ := h.seedUser(t, "ada@example.com", "Ada", "owner")
	memberID, memberCookie, _ := h.seedUser(t, "bob@example.com", "Bob", "member")
	pngBytes := buildPNG(t, 16, 16, color.NRGBA{R: 40, G: 80, B: 120, A: 255})

	// 1. Disabling via DELETE /api/members/{id} hides the avatar (404) while keeping DB key and file on disk.
	up := h.putMultipart(t, "/api/me/avatar", memberCookie, "", "avatar", "bob.png", "image/png", pngBytes)
	if up.Code != http.StatusOK {
		t.Fatalf("member upload status = %d", up.Code)
	}
	bobKey := keyFromAvatarURL(t, parseAvatarURLResponse(t, up))
	dis := h.doRequest(http.MethodDelete, "/api/members/"+memberID.String(), ownerCookie, "", nil, nil)
	if dis.Code != http.StatusNoContent {
		t.Fatalf("disable member status = %d body = %s", dis.Code, dis.Body.String())
	}
	if r := h.doRequest(http.MethodGet, "/api/users/"+memberID.String()+"/avatar", ownerCookie, "", nil, nil); r.Code != http.StatusNotFound {
		t.Fatalf("GET disabled member avatar status = %d, want 404", r.Code)
	}
	if got := h.avatarKeyOf(t, memberID); got != bobKey {
		t.Fatalf("disable cleared avatar_key: got %q, want %q", got, bobKey)
	}
	if _, err := os.Stat(filepath.Join(h.avatarsDir, bobKey)); err != nil {
		t.Fatalf("disable deleted avatar blob: %v", err)
	}

	// 2. Re-inviting the disabled member clears avatar_key and deletes the old blob.
	var memberRoleID string
	if err := h.owner.QueryRow(h.ctx, `SELECT id::text FROM identity.roles WHERE key = 'member'`).Scan(&memberRoleID); err != nil {
		t.Fatal(err)
	}
	reinviteBody, _ := json.Marshal(map[string]string{
		"email": "bob@example.com", "name": "Bob Reinvited", "role_id": memberRoleID,
	})
	reinv := h.doRequest(http.MethodPost, "/api/members", ownerCookie, "", map[string]string{
		"Content-Type": "application/json",
	}, bytes.NewReader(reinviteBody))
	if reinv.Code != http.StatusOK {
		t.Fatalf("re-invite status = %d body = %s", reinv.Code, reinv.Body.String())
	}
	if got := h.avatarKeyOf(t, memberID); got != "" {
		t.Fatalf("re-invite kept avatar_key = %q, want empty", got)
	}
	if _, err := os.Stat(filepath.Join(h.avatarsDir, bobKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("re-invite left old blob on disk: %v", err)
	}

	// 3. Provisioning deactivate keeps avatar (hidden with 404), re-activate restores it,
	//    and provisioning DeleteMember clears avatar_key and removes the blob from disk.
	provMember, err := h.prov.CreateMember(h.ctx, identity.ProvisionMemberInput{
		ExternalID: "ext-cara", Email: "cara@example.com", Name: "Cara", Role: "member", KeyID: "k1",
	})
	if err != nil {
		t.Fatal(err)
	}
	caraID, err := domain.ParseUserID(provMember.ID)
	if err != nil {
		t.Fatal(err)
	}
	caraCookie, _, err := h.sessions.Start(h.ctx, caraID, "sso", "")
	if err != nil {
		t.Fatal(err)
	}
	upCara := h.putMultipart(t, "/api/me/avatar", caraCookie, "", "avatar", "cara.png", "image/png", pngBytes)
	if upCara.Code != http.StatusOK {
		t.Fatalf("cara upload status = %d", upCara.Code)
	}
	caraKey := keyFromAvatarURL(t, parseAvatarURLResponse(t, upCara))

	inactive := false
	if _, err := h.prov.UpdateMember(h.ctx, "ext-cara", identity.ProvisionMemberUpdate{
		Email: "cara@example.com", Name: "Cara", Role: "member", Active: &inactive, KeyID: "k1",
	}); err != nil {
		t.Fatal(err)
	}
	if r := h.doRequest(http.MethodGet, "/api/users/"+caraID.String()+"/avatar", ownerCookie, "", nil, nil); r.Code != http.StatusNotFound {
		t.Fatalf("GET deactivated provisioned member avatar = %d, want 404", r.Code)
	}
	if _, err := os.Stat(filepath.Join(h.avatarsDir, caraKey)); err != nil {
		t.Fatalf("deactivate deleted blob: %v", err)
	}
	active := true
	if _, err := h.prov.UpdateMember(h.ctx, "ext-cara", identity.ProvisionMemberUpdate{
		Email: "cara@example.com", Name: "Cara", Role: "member", Active: &active, KeyID: "k1",
	}); err != nil {
		t.Fatal(err)
	}
	if r := h.doRequest(http.MethodGet, "/api/users/"+caraID.String()+"/avatar", ownerCookie, "", nil, nil); r.Code != http.StatusOK {
		t.Fatalf("GET reactivated provisioned member avatar = %d, want 200", r.Code)
	}

	if err := h.prov.DeleteMember(h.ctx, "ext-cara", "k1"); err != nil {
		t.Fatal(err)
	}
	if got := h.avatarKeyOf(t, caraID); got != "" {
		t.Fatalf("DeleteMember left avatar_key = %q, want empty", got)
	}
	if _, err := os.Stat(filepath.Join(h.avatarsDir, caraKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("DeleteMember left avatar blob on disk: %v", err)
	}
	if r := h.doRequest(http.MethodGet, "/api/users/"+caraID.String()+"/avatar", ownerCookie, "", nil, nil); r.Code != http.StatusNotFound {
		t.Fatalf("GET deleted member avatar = %d, want 404", r.Code)
	}

}

func TestAvatarAuditAndThrottle(t *testing.T) {
	h := newAvatarRig(t)
	userA, cookieA, _ := h.seedUser(t, "ada@example.com", "Ada", "owner")
	pngBytes := buildPNG(t, 8, 8, color.NRGBA{R: 77, G: 88, B: 99, A: 255})

	// 1st write: PUT -> avatar.set.
	if r := h.putMultipart(t, "/api/me/avatar", cookieA, "", "avatar", "a.png", "image/png", pngBytes); r.Code != http.StatusOK {
		t.Fatalf("PUT 1 status = %d body = %s", r.Code, r.Body.String())
	}
	// 2nd write: DELETE -> avatar.remove.
	if r := h.doRequest(http.MethodDelete, "/api/me/avatar", cookieA, "", nil, nil); r.Code != http.StatusNoContent {
		t.Fatalf("DELETE 2 status = %d body = %s", r.Code, r.Body.String())
	}

	// Verify audit rows recorded for both actions without raw image bytes.
	rows, err := h.owner.Query(h.ctx, `
		SELECT action, outcome, target_type, target_id, changes::text
		FROM audit.pending
		WHERE action IN ('avatar.set', 'avatar.remove')
		ORDER BY id ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, outcome, targetType, targetID, changes string
		if err := rows.Scan(&action, &outcome, &targetType, &targetID, &changes); err != nil {
			t.Fatal(err)
		}
		if outcome != "success" || targetType != "user" || targetID != userA.String() {
			t.Fatalf("unexpected audit row: action=%s outcome=%s target=%s/%s", action, outcome, targetType, targetID)
		}
		if strings.Contains(changes, "PNG") {
			t.Fatalf("audit payload leaked image bytes: %s", changes)
		}
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0] != "avatar.set" || actions[1] != "avatar.remove" {
		t.Fatalf("audit actions = %v, want [avatar.set avatar.remove]", actions)
	}

	// Remaining 8 burst writes (total 10) succeed; 11th returns 429 with Retry-After.
	for i := 3; i <= 10; i++ {
		if r := h.putMultipart(t, "/api/me/avatar", cookieA, "", "avatar", "a.png", "image/png", pngBytes); r.Code != http.StatusOK {
			t.Fatalf("write %d status = %d, want 200", i, r.Code)
		}
	}
	throttled := h.putMultipart(t, "/api/me/avatar", cookieA, "", "avatar", "a.png", "image/png", pngBytes)
	if throttled.Code != http.StatusTooManyRequests {
		t.Fatalf("11th write status = %d, want 429, body = %s", throttled.Code, throttled.Body.String())
	}
	if throttled.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header on 429")
	}
}
