package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCookieAttributes(t *testing.T) {
	rec := httptest.NewRecorder()
	SetCookie(rec, "abc", 120)
	res := rec.Result()
	cookies := res.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != CookieName || cookie.Value != "abc" || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("cookie = %+v", cookie)
	}
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 120 {
		t.Fatalf("cookie flags = secure %v httpOnly %v sameSite %v maxAge %d", cookie.Secure, cookie.HttpOnly, cookie.SameSite, cookie.MaxAge)
	}
}
