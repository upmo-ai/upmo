package domain

import (
	"crypto/sha256"
	"strings"

	"golang.org/x/text/unicode/norm"
)

func ClaimHash(claim string) [32]byte {
	claim = norm.NFKC.String(claim)
	claim = strings.ToLower(claim)
	claim = strings.Join(strings.Fields(claim), " ")
	claim = strings.TrimRight(claim, ".!?")
	return sha256.Sum256([]byte(claim))
}
