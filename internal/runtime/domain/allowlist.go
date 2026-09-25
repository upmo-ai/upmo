package domain

import (
	"strconv"
	"strings"
)

const (
	// OpGrant adds a host name to a task allow-list.
	OpGrant = "grant"
	// OpRevoke removes a host name from a task allow-list.
	OpRevoke = "revoke"
)

// Change records which person granted or revoked which host name.
type Change struct {
	Person string
	Op     string
	Host   string
}

// Request records one egress decision. It never carries a path or a query.
type Request struct {
	Host    string
	Port    string
	Verdict string
	Reason  string
}

// OpenList returns the empty allow-list a task starts with, which refuses every name.
func OpenList() []string {
	return nil
}

// Grant returns a copy of list with host appended. On error it returns list unchanged.
func Grant(list []string, person, host string) ([]string, Change, error) {
	change, err := newChange(OpGrant, person, host)
	if err != nil || indexOf(list, change.Host) >= 0 {
		return list, Change{}, ErrEgress
	}

	return append(append(make([]string, 0, len(list)+1), list...), change.Host), change, nil
}

// Revoke returns a copy of list without host, keeping the order of the rest.
// On error it returns list unchanged.
func Revoke(list []string, person, host string) ([]string, Change, error) {
	change, err := newChange(OpRevoke, person, host)
	if err != nil {
		return list, Change{}, ErrEgress
	}
	i := indexOf(list, change.Host)
	if i < 0 {
		return list, Change{}, ErrEgress
	}

	return append(append(make([]string, 0, len(list)-1), list[:i]...), list[i+1:]...), change, nil
}

// Note copies a decision into a request record after checking it is well formed.
func Note(decision Decision) (Request, error) {
	switch d := decision; {
	case d.Verdict != VerdictAllow && d.Verdict != VerdictRefuse,
		d.Reason != ReasonListed && d.Reason != ReasonAllowlist && d.Reason != ReasonHost && d.Reason != ReasonAddress,
		(d.Verdict == VerdictAllow) != (d.Reason == ReasonListed),
		d.Host == "" || strings.ContainsAny(d.Host, "/?#@ "),
		d.Port != "" && !validPort(d.Port):
		return Request{}, ErrEgress
	}

	return Request{Host: decision.Host, Port: decision.Port, Verdict: decision.Verdict, Reason: decision.Reason}, nil
}

// newChange checks the person and host, and normalises the host through AdmitEgress.
func newChange(op, person, host string) (Change, error) {
	person = strings.TrimSpace(person)
	if person == "" || len(person) > 64 || strings.ContainsAny(person, " \t/") {
		return Change{}, ErrEgress
	}
	d, err := AdmitEgress(host, []string{host})
	if err != nil || d.Verdict != VerdictAllow || d.Reason != ReasonListed || d.Port != "" {
		return Change{}, ErrEgress
	}

	return Change{Person: person, Op: op, Host: d.Host}, nil
}

// indexOf returns the position of host in list, or -1.
func indexOf(list []string, host string) int {
	for i, entry := range list {
		if entry == host {
			return i
		}
	}

	return -1
}

// validPort reports a decimal port from 1 to 65535 with no leading zero.
func validPort(port string) bool {
	n, err := strconv.Atoi(port)

	return err == nil && port[0] != '0' && strings.Trim(port, "0123456789") == "" && n >= 1 && n <= 65535
}
