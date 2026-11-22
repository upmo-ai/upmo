package domain

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
)

const (
	// VerdictAllow lets a request through.
	VerdictAllow = "allow"
	// VerdictRefuse stops a request.
	VerdictRefuse = "refuse"
	// ReasonListed means the name is an allow entry.
	ReasonListed = "listed"
	// ReasonAllowlist means the name is not an allow entry.
	ReasonAllowlist = "allowlist"
	// ReasonHost means the name refers to the host or its local network.
	ReasonHost = "host"
	// ReasonAddress means the request gave a literal address.
	ReasonAddress = "address"
)

// ErrEgress reports a malformed request host or allow entry.
var ErrEgress = errors.New("runtime: egress")

// Decision records the outcome for one request host.
type Decision struct {
	Verdict string
	Host    string
	Port    string
	Reason  string
}

// AdmitEgress decides whether a task may reach host, given the allow entries.
// Every allow entry is validated before any refusal is returned.
func AdmitEgress(host string, allow []string) (Decision, error) {
	name, port, ok := parseEgress(host)
	if !ok {
		return Decision{}, ErrEgress
	}

	listed := false
	for _, entry := range allow {
		entryName, entryPort, ok := parseEgress(entry)
		if !ok || entryPort != "" || isAddress(entryName) || isHostLocal(entryName) {
			return Decision{}, ErrEgress
		}
		listed = listed || entryName == name
	}

	decision := Decision{Verdict: VerdictRefuse, Host: name, Port: port}
	switch {
	case isAddress(name):
		decision.Reason = ReasonAddress
	case isHostLocal(name):
		decision.Reason = ReasonHost
	case !listed:
		decision.Reason = ReasonAllowlist
	default:
		decision.Verdict, decision.Reason = VerdictAllow, ReasonListed
	}

	return decision, nil
}

// parseEgress splits raw into a normalized name and an optional port.
// It reports false for anything that is neither an address nor a valid DNS name.
func parseEgress(raw string) (name, port string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, "@/\\?#*") || strings.IndexFunc(s, func(r rune) bool {
		return r > unicode.MaxASCII || unicode.IsSpace(r)
	}) >= 0 {
		return "", "", false
	}

	name, hasPort, bracket := s, false, strings.HasPrefix(s, "[")
	if bracket {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return "", "", false
		}
		name = s[1:end]
		if rest := s[end+1:]; rest != "" {
			if port, hasPort = strings.CutPrefix(rest, ":"); !hasPort {
				return "", "", false
			}
		}
	} else if strings.Count(s, ":") == 1 {
		name, port, hasPort = strings.Cut(s, ":")
	}

	if hasPort {
		n, err := strconv.Atoi(port)
		if port == "" || port[0] == '0' || strings.Trim(port, "0123456789") != "" || err != nil || n > 65535 {
			return "", "", false
		}
	}

	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if !isAddress(name) && (bracket || !validName(name)) {
		return "", "", false
	}

	return name, port, true
}

// isAddress recognizes a literal address, including the shortened, octal and
// hexadecimal IPv4 forms that resolvers still accept. The name is already lowercase.
func isAddress(name string) bool {
	if _, err := netip.ParseAddr(name); err == nil {
		return true
	}

	parts := strings.Split(name, ".")
	if len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		digits := "0123456789"
		if rest, hex := strings.CutPrefix(part, "0x"); hex {
			part, digits = rest, "0123456789abcdef"
		}
		if part == "" || strings.Trim(part, digits) != "" {
			return false
		}
	}
	if len(parts) == 1 && !strings.HasPrefix(name, "0x") {
		_, err := strconv.ParseUint(name, 10, 32)
		return err == nil
	}

	return true
}

// isHostLocal reports a single label, which includes localhost, or a reserved local suffix.
func isHostLocal(name string) bool {
	if !strings.Contains(name, ".") {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".localdomain", ".internal", ".home.arpa"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}

	return false
}

// validName reports whether name is a well formed lowercase DNS name.
func validName(name string) bool {
	if len(name) < 1 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' ||
			strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return false
		}
	}

	return true
}
