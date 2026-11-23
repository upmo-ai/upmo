package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

var defaultAvatarUploadSlots = make(chan struct{}, 2)

// Avatars serves user avatar upload, removal, and read routes.
type Avatars struct {
	UploadSlots chan struct{}
	Members     app.Members
	Sessions    app.Service
	Limit       interface {
		Take(ctx context.Context, key string, now time.Time, burst, perSecond float64) (bool, time.Duration, error)
	}
	Now func() time.Time
}

// Mount registers the avatar routes.
func (h Avatars) Mount(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/me/avatar", h.put)
	mux.HandleFunc("DELETE /api/me/avatar", h.remove)
	mux.HandleFunc("GET /api/users/{id}/avatar", h.get)
}

func (h Avatars) put(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	if !h.take(w, r, session) {
		return
	}
	slots := h.UploadSlots
	if slots == nil {
		slots = defaultAvatarUploadSlots
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-r.Context().Done():
		return
	}

	raw, status, msg, reason := readAvatarUpload(w, r)
	if status != 0 {
		audit.Annotate(r.Context(), audit.Event{
			TargetType: "user",
			TargetID:   session.UserID.String(),
			Reason:     reason,
		})
		writeAvatarError(w, status, msg)
		return
	}

	sniffed := http.DetectContentType(raw)
	if !domain.IsAllowedAvatarType(sniffed) {
		audit.Annotate(r.Context(), audit.Event{
			TargetType: "user",
			TargetID:   session.UserID.String(),
			Reason:     "unsupported_media_type",
		})
		writeAvatarError(w, http.StatusUnsupportedMediaType, "avatar must be a PNG, JPEG, GIF, or WebP image")
		return
	}

	avatarURL, err := h.Members.SetAvatar(r.Context(), session, raw)
	if err != nil {
		h.refuseAvatarWrite(w, r, session, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"avatar_url": avatarURL})
}

func (h Avatars) remove(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	if !h.take(w, r, session) {
		return
	}
	if err := h.Members.RemoveAvatar(r.Context(), session); err != nil {
		h.refuseAvatarWrite(w, r, session, err)
		return
	}
	noContent(w)
}

func (h Avatars) get(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session(w, r); !ok {
		return
	}
	userID, err := domain.ParseUserID(r.PathValue("id"))
	if err != nil {
		writeAvatarError(w, http.StatusNotFound, "no such avatar")
		return
	}
	avatarKey, pngBytes, err := h.Members.ReadAvatar(r.Context(), userID)
	if errors.Is(err, domain.ErrAvatarNotFound) {
		writeAvatarError(w, http.StatusNotFound, "no such avatar")
		return
	}
	if err != nil {
		http.Error(w, "avatar", http.StatusInternalServerError)
		return
	}

	etag := `"` + avatarKey + `"`
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `inline; filename="avatar.png"`)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("ETag", etag)
	if r.URL.Query().Get("v") == avatarKey {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	if ifNoneMatchMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(pngBytes)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pngBytes) //nolint:gosec // G705: server-re-encoded PNG served with image/png, nosniff, and sandbox CSP
}

func (h Avatars) session(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	if !allowPerson(w, r) {
		return app.Session{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		writeRaw(w, http.StatusUnauthorized, unauthorized)
		return app.Session{}, false
	}
	session, err := h.Sessions.Lookup(r.Context(), cookie.Value)
	if err != nil {
		ClearCookie(w)
		writeRaw(w, http.StatusUnauthorized, unauthorized)
		return app.Session{}, false
	}
	markActor(r, session)
	return session, true
}

func (h Avatars) take(w http.ResponseWriter, r *http.Request, session app.Session) bool {
	if h.Limit == nil {
		return true
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	allowed, retry, err := h.Limit.Take(r.Context(), "avatar:"+session.UserID.String(), now, domain.AvatarThrottleBurst, domain.AvatarThrottlePerSecond)
	if err != nil {
		http.Error(w, "throttle", http.StatusInternalServerError)
		return false
	}
	if !allowed {
		audit.Annotate(r.Context(), audit.Event{
			TargetType: "user",
			TargetID:   session.UserID.String(),
			Reason:     "throttled",
		})
		secs := int(retry.Round(time.Second) / time.Second)
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeRaw(w, http.StatusTooManyRequests, slowDown)
		return false
	}
	return true
}

func (h Avatars) refuseAvatarWrite(w http.ResponseWriter, r *http.Request, session app.Session, err error) {
	var (
		status int
		msg    string
		reason string
	)
	switch {
	case errors.Is(err, domain.ErrAvatarEmpty):
		status, msg, reason = http.StatusBadRequest, "an avatar needs an image", "empty_avatar"
	case errors.Is(err, domain.ErrAvatarTooLarge):
		status, msg, reason = http.StatusRequestEntityTooLarge, "avatar must be 50 MB or smaller", "too_large"
	case errors.Is(err, domain.ErrAvatarUnsupportedMedia):
		status, msg, reason = http.StatusUnsupportedMediaType, "avatar must be a PNG, JPEG, GIF, or WebP image", "unsupported_media_type"
	case errors.Is(err, domain.ErrAvatarDimensions):
		status, msg, reason = http.StatusUnprocessableEntity, "image dimensions are too large", "invalid_dimensions"
	case errors.Is(err, domain.ErrAvatarCorrupt):
		status, msg, reason = http.StatusUnprocessableEntity, "image could not be decoded", "corrupt_image"
	case errors.Is(err, app.ErrNotSignedIn):
		ClearCookie(w)
		writeRaw(w, http.StatusUnauthorized, unauthorized)
		return
	default:
		http.Error(w, "avatar", http.StatusInternalServerError)
		return
	}
	audit.Annotate(r.Context(), audit.Event{
		TargetType: "user",
		TargetID:   session.UserID.String(),
		Reason:     reason,
	})
	writeAvatarError(w, status, msg)
}

func readAvatarUpload(w http.ResponseWriter, r *http.Request) ([]byte, int, string, string) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return nil, http.StatusBadRequest, "avatar must be uploaded as multipart/form-data", "bad_request"
	}

	r.Body = http.MaxBytesReader(w, r.Body, domain.MaxAvatarUploadBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, http.StatusBadRequest, "avatar must be uploaded as multipart/form-data", "bad_request"
	}

	var (
		raw  []byte
		seen bool
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if isMaxBytesError(err) {
			return nil, http.StatusRequestEntityTooLarge, "avatar must be 50 MB or smaller", "too_large"
		}
		if err != nil {
			return nil, http.StatusBadRequest, "invalid multipart upload", "bad_request"
		}
		if seen {
			_ = part.Close()
			return nil, http.StatusBadRequest, "upload must contain exactly one avatar file", "bad_request"
		}
		if part.FormName() != "avatar" || part.FileName() == "" {
			_ = part.Close()
			return nil, http.StatusBadRequest, "upload must contain a single file part named avatar", "bad_request"
		}

		data, readErr := io.ReadAll(io.LimitReader(part, domain.MaxAvatarUploadBytes+1))
		_ = part.Close()
		if isMaxBytesError(readErr) || len(data) > domain.MaxAvatarUploadBytes {
			return nil, http.StatusRequestEntityTooLarge, "avatar must be 50 MB or smaller", "too_large"
		}
		if readErr != nil {
			return nil, http.StatusBadRequest, "invalid multipart upload", "bad_request"
		}
		raw = data
		seen = true
	}

	if !seen {
		return nil, http.StatusBadRequest, "upload must contain a single file part named avatar", "bad_request"
	}
	if len(raw) == 0 {
		return nil, http.StatusBadRequest, "an avatar needs an image", "empty_avatar"
	}
	return raw, 0, "", ""
}

func isMaxBytesError(err error) bool {
	if err == nil {
		return false
	}
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

func writeAvatarError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func ifNoneMatchMatches(header, etag string) bool {
	if strings.TrimSpace(header) == "" {
		return false
	}
	for _, item := range strings.Split(header, ",") {
		candidate := strings.TrimSpace(item)
		if candidate == "*" || candidate == etag || candidate == "W/"+etag {
			return true
		}
	}
	return false
}
