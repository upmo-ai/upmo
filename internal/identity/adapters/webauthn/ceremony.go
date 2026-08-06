// Package webauthn runs the passkey ceremonies for the identity context.
package webauthn

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"
	wan "github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// Ceremony is the relying party.
type Ceremony struct {
	wa *wan.WebAuthn
}

// New builds a relying party for one origin.
func New(rpID, origin string) (Ceremony, error) {
	wa, err := wan.New(&wan.Config{
		RPID:          rpID,
		RPDisplayName: "Upmo",
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
		AttestationPreference: protocol.PreferNoAttestation,
	})
	if err != nil {
		return Ceremony{}, fmt.Errorf("identity: webauthn: %w", err)
	}
	return Ceremony{wa: wa}, nil
}

// BeginDiscoverable starts a passkey sign-in with no username.
func (c Ceremony) BeginDiscoverable() (options, session []byte, err error) {
	assertion, data, err := c.wa.BeginDiscoverableLogin(wan.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, nil, err
	}
	options, err = json.Marshal(assertion)
	if err != nil {
		return nil, nil, err
	}
	session, err = json.Marshal(data)
	return options, session, err
}

// FinishDiscoverable checks a sign-in. lookup loads the person who holds the credential.
func (c Ceremony) FinishDiscoverable(session, body []byte, lookup func(credentialID, userHandle []byte) (domain.Account, error)) (domain.Account, domain.Credential, error) {
	var data wan.SessionData
	if err := json.Unmarshal(session, &data); err != nil {
		return domain.Account{}, domain.Credential{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return domain.Account{}, domain.Credential{}, err
	}
	user, credential, err := c.wa.ValidatePasskeyLogin(func(rawID, userHandle []byte) (wan.User, error) {
		account, err := lookup(rawID, userHandle)
		if err != nil {
			return nil, err
		}
		return persona{account}, nil
	}, data, parsed)
	if err != nil {
		return domain.Account{}, domain.Credential{}, err
	}
	account := user.(persona).account
	return account, fromLibrary(*credential), nil
}

// BeginRegistration starts a new passkey for account.
func (c Ceremony) BeginRegistration(account domain.Account) (options, session []byte, err error) {
	creation, data, err := c.wa.BeginRegistration(
		persona{account},
		wan.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		wan.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
	)
	if err != nil {
		return nil, nil, err
	}
	options, err = json.Marshal(creation)
	if err != nil {
		return nil, nil, err
	}
	session, err = json.Marshal(data)
	return options, session, err
}

// FinishRegistration checks a new passkey.
func (c Ceremony) FinishRegistration(account domain.Account, session, body []byte) (domain.Credential, error) {
	var data wan.SessionData
	if err := json.Unmarshal(session, &data); err != nil {
		return domain.Credential{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		return domain.Credential{}, err
	}
	credential, err := c.wa.CreateCredential(persona{account}, data, parsed)
	if err != nil {
		return domain.Credential{}, err
	}
	return fromLibrary(*credential), nil
}

type persona struct {
	account domain.Account
}

func (p persona) WebAuthnID() []byte { return p.account.Handle }

func (p persona) WebAuthnName() string { return p.account.Name }

func (p persona) WebAuthnDisplayName() string { return p.account.Display }

func (p persona) WebAuthnCredentials() []wan.Credential {
	out := make([]wan.Credential, 0, len(p.account.Credentials))
	for _, item := range p.account.Credentials {
		out = append(out, toLibrary(item))
	}
	return out
}

func toLibrary(item domain.Credential) wan.Credential {
	return wan.Credential{
		ID:                item.ID,
		PublicKey:         item.PublicKey,
		AttestationFormat: item.Attestation,
		Flags: wan.CredentialFlags{
			UserPresent:    true,
			UserVerified:   true,
			BackupEligible: item.BackupEligible,
			BackupState:    item.BackupState,
		},
		Authenticator: wan.Authenticator{
			AAGUID:    item.AAGUID[:],
			SignCount: item.SignCount,
		},
	}
}

func fromLibrary(item wan.Credential) domain.Credential {
	var aaguid uuid.UUID
	copy(aaguid[:], item.Authenticator.AAGUID)
	transports := make([]string, 0, len(item.Transport))
	for _, transport := range item.Transport {
		transports = append(transports, string(transport))
	}
	return domain.Credential{
		ID:             bytes.Clone(item.ID),
		PublicKey:      bytes.Clone(item.PublicKey),
		SignCount:      item.Authenticator.SignCount,
		AAGUID:         aaguid,
		Transports:     transports,
		BackupEligible: item.Flags.BackupEligible,
		BackupState:    item.Flags.BackupState,
		Attestation:    item.AttestationFormat,
		CloneWarning:   item.Authenticator.CloneWarning,
	}
}

// Challenge is the challenge text inside a stored ceremony.
func Challenge(session []byte) (string, error) {
	var data wan.SessionData
	if err := json.Unmarshal(session, &data); err != nil {
		return "", err
	}
	return data.Challenge, nil
}
