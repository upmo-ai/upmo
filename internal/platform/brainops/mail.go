package brainops

import (
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/mail"
)

// LinkMail returns the sender for invite and recover links. With SMTP unset it
// sends nothing and the links are only copied. Production (the zero Env)
// requires STARTTLS on every host against UPMO_TLS_CA_FILE.
func LinkMail(env config.Env, s config.SMTP) mail.Sender {
	if s.Host == "" {
		return mail.Noop{}
	}
	return mail.SMTP{
		Host:        s.Host,
		Port:        s.Port,
		User:        s.User,
		Password:    s.Password,
		From:        s.From,
		Development: env == config.EnvDevelopment,
		CAFile:      s.CAFile,
	}
}
