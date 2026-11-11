package audit

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// The export pins OCSF 1.3.0; the golden test fixes the exact bytes.
const ocsfVersion = "1.3.0"

// OCSF status ids: one for success, two for refused or failed.
const (
	ocsfStatusSuccess = 1
	ocsfStatusProblem = 2
)

// Sealed is one event with the chain fields the sealer gave it.
type Sealed struct {
	Row
	Position int64
	PrevHash []byte
	Hash     []byte
}

// OCSF maps one sealed event onto the OCSF schema as the stream line carries
// it. An action outside the catalog is refused.
func OCSF(sealed Sealed) ([]byte, error) {
	def, ok := catalog[sealed.Action]
	if !ok {
		return nil, fmt.Errorf("audit: action %q is not in the catalog", sealed.Action)
	}
	status := ocsfStatusProblem
	if sealed.Outcome == Success {
		status = ocsfStatusSuccess
	}
	detail := string(sealed.Outcome)
	if sealed.Reason != "" {
		detail += ": " + sealed.Reason
	}
	return json.Marshal(ocsfEvent{
		Stream:       "audit",
		ClassUID:     def.class,
		ActivityID:   def.activity,
		Time:         sealed.OccurredAt.UTC().UnixMilli(),
		StatusID:     status,
		StatusDetail: detail,
		Metadata: ocsfMetadata{
			UID:      sealed.ID,
			Sequence: sealed.Position,
			Product:  ocsfProduct{Name: "Upmo"},
			Version:  ocsfVersion,
		},
		Actor: ocsfActor{
			User:    ocsfUser{UID: sealed.ActorID},
			Session: ocsfSession{UID: sealed.CredentialID},
		},
		SrcEndpoint: ocsfEndpoint{IP: sealed.SourceIP},
		HTTPRequest: ocsfRequest{UserAgent: sealed.UserAgent},
		API:         ocsfAPI{Operation: string(sealed.Action)},
		Resources:   []ocsfResource{{Type: sealed.TargetType, UID: sealed.TargetID}},
		Unmapped: map[string]any{
			"changes":    normalChanges(sealed.Changes),
			"request_id": sealed.RequestID,
			"prev_hash":  hex.EncodeToString(sealed.PrevHash),
			"hash":       hex.EncodeToString(sealed.Hash),
		},
	})
}

type ocsfEvent struct {
	Stream       string         `json:"stream"`
	ClassUID     int            `json:"class_uid"`
	ActivityID   int            `json:"activity_id"`
	Time         int64          `json:"time"`
	StatusID     int            `json:"status_id"`
	StatusDetail string         `json:"status_detail"`
	Metadata     ocsfMetadata   `json:"metadata"`
	Actor        ocsfActor      `json:"actor"`
	SrcEndpoint  ocsfEndpoint   `json:"src_endpoint"`
	HTTPRequest  ocsfRequest    `json:"http_request"`
	API          ocsfAPI        `json:"api"`
	Resources    []ocsfResource `json:"resources"`
	Unmapped     map[string]any `json:"unmapped"`
}

type ocsfMetadata struct {
	UID      string      `json:"uid"`
	Sequence int64       `json:"sequence"`
	Product  ocsfProduct `json:"product"`
	Version  string      `json:"version"`
}

type ocsfProduct struct {
	Name string `json:"name"`
}

type ocsfActor struct {
	User    ocsfUser    `json:"user"`
	Session ocsfSession `json:"session"`
}

type ocsfUser struct {
	UID string `json:"uid"`
}

type ocsfSession struct {
	UID string `json:"uid"`
}

type ocsfEndpoint struct {
	IP string `json:"ip"`
}

type ocsfRequest struct {
	UserAgent string `json:"user_agent"`
}

type ocsfAPI struct {
	Operation string `json:"operation"`
}

type ocsfResource struct {
	Type string `json:"type"`
	UID  string `json:"uid"`
}
