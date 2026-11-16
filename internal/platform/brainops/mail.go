package brainops

import (
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/mail"
)

// LinkMail returns the sender for invite and recover links. With SMTP unset it
// sends nothing and the links are only copied.
func LinkMail(s config.SMTP) mail.Sender {
	if s.Host == "" {
		return mail.Noop{}
	}
	return mail.SMTP{Host: s.Host, Port: s.Port, User: s.User, Password: s.Password, From: s.From}
}
