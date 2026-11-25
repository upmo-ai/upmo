package brainops

import (
	"testing"

	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/mail"
)

func TestLinkMailIsNoopWhenSMTPIsUnset(t *testing.T) {
	if _, ok := LinkMail(config.EnvProduction, config.SMTP{}).(mail.Noop); !ok {
		t.Fatal("unset SMTP is not the noop sender")
	}
}

func TestLinkMailCarriesTheEnvironmentAndCAFile(t *testing.T) {
	s := config.SMTP{Host: "smtp.example.com", Port: 587, From: "noreply@example.com", CAFile: "/etc/upmo/ca.pem"}
	prod, ok := LinkMail(config.EnvProduction, s).(mail.SMTP)
	if !ok || prod.Host != "smtp.example.com" || prod.Port != 587 || prod.From != "noreply@example.com" || prod.Development || prod.CAFile != "/etc/upmo/ca.pem" {
		t.Fatalf("production sender = %#v", prod)
	}
	dev, ok := LinkMail(config.EnvDevelopment, s).(mail.SMTP)
	if !ok || !dev.Development || dev.CAFile != "/etc/upmo/ca.pem" {
		t.Fatalf("development sender = %#v", dev)
	}
}
