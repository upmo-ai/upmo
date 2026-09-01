package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type Candidate struct {
	Claim           string
	ChangesBehavior bool
}

type Input struct {
	PreparedSource  string
	Candidate       Candidate
	DenyClaims      []string
	ActiveHashes    [][32]byte
	ForgottenHashes [][32]byte
}

type Action string

const (
	ActionDrop      Action = "drop"
	ActionReinforce Action = "reinforce"
	ActionKeep      Action = "keep"
)

type Result struct {
	Action Action
	Reason string
	Hash   [32]byte
}

var contentStopWords = map[string]struct{}{
	"the":  {},
	"that": {},
	"this": {},
	"with": {},
	"from": {},
	"have": {},
	"your": {},
}

func Gate(in Input) Result {
	if !in.Candidate.ChangesBehavior {
		return Result{Action: ActionDrop, Reason: "general"}
	}

	claim := Redact(in.Candidate.Claim)
	normalizedClaim := strings.Join(strings.Fields(strings.ToLower(claim)), " ")
	if containsAny(normalizedClaim, "from now on", "you must", "ignore previous") {
		return Result{Action: ActionDrop, Reason: "imperative"}
	}
	if containsAny(normalizedClaim, "last 24 hours", "currently", "today") {
		return Result{Action: ActionDrop, Reason: "bounded_window"}
	}

	candidateWords := contentWords(claim)
	if echoesDenyClaim(candidateWords, in.DenyClaims) {
		return Result{Action: ActionDrop, Reason: "echo"}
	}

	sourceWords := contentWords(Redact(in.PreparedSource))
	if !setsOverlap(candidateWords, sourceWords) {
		return Result{Action: ActionDrop, Reason: "ungrounded"}
	}

	hash := ClaimHash(claim)
	if containsHash(in.ActiveHashes, hash) {
		return Result{Action: ActionReinforce, Reason: "active", Hash: hash}
	}
	if containsHash(in.ForgottenHashes, hash) {
		return Result{Action: ActionDrop, Reason: "forgotten", Hash: hash}
	}
	return Result{Action: ActionKeep, Reason: "new", Hash: hash}
}

func containsAny(s string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

func contentWords(s string) map[string]struct{} {
	words := make(map[string]struct{})
	var word strings.Builder
	addWord := func() {
		if word.Len() == 0 {
			return
		}
		value := strings.ToLower(word.String())
		word.Reset()
		if utf8.RuneCountInString(value) < 4 {
			return
		}
		if _, stopped := contentStopWords[value]; stopped {
			return
		}
		words[value] = struct{}{}
	}

	for _, r := range s {
		if unicode.IsLetter(r) {
			word.WriteRune(r)
			continue
		}
		addWord()
	}
	addWord()
	return words
}

func echoesDenyClaim(candidate map[string]struct{}, denyClaims []string) bool {
	if len(candidate) == 0 {
		return false
	}
	for _, denyClaim := range denyClaims {
		denied := contentWords(Redact(denyClaim))
		overlap := overlapCount(candidate, denied)
		if overlap*10 <= len(candidate)*7 {
			continue
		}
		if isStrictSuperset(candidate, denied) {
			continue
		}
		return true
	}
	return false
}

func isStrictSuperset(candidate, denied map[string]struct{}) bool {
	if len(candidate) <= len(denied) {
		return false
	}
	for word := range denied {
		if _, ok := candidate[word]; !ok {
			return false
		}
	}
	return true
}

func setsOverlap(a, b map[string]struct{}) bool {
	return overlapCount(a, b) > 0
}

func overlapCount(a, b map[string]struct{}) int {
	count := 0
	for word := range a {
		if _, ok := b[word]; ok {
			count++
		}
	}
	return count
}

func containsHash(hashes [][32]byte, target [32]byte) bool {
	for _, hash := range hashes {
		if hash == target {
			return true
		}
	}
	return false
}
