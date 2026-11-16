package brainops

import (
	"testing"

	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/mail"
)

func TestLinkMailIsNoopWhenSMTPIsUnset(t *testing.T) {
	if _, ok := LinkMail(config.SMTP{}).(mail.Noop); !ok {
		t.Fatal("unset SMTP is not the noop sender")
	}
}

func TestLinkMailUsesTheConfiguredRelay(t *testing.T) {
	got, ok := LinkMail(config.SMTP{Host: "smtp.example.com", Port: 587, From: "noreply@example.com"}).(mail.SMTP)
	if !ok || got.Host != "smtp.example.com" || got.Port != 587 || got.From != "noreply@example.com" {
		t.Fatalf("sender = %#v", got)
	}
}
