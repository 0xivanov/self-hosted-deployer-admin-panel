// Package domains contains domain resale rules shared by registrar adapters.
package domains

import (
	"errors"
	"strings"
)

var ErrDomain = errors.New("enter a supported domain name without a URL or subdomain")

// PurchaseName limits the initial product to standard ASCII second-level names
// in com/net/org. IDNs, multi-label suffixes and premium names need explicit
// product/registrar support; they must not be silently rewritten into a purchase.
func PurchaseName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	parts := strings.Split(name, ".")
	if len(parts) != 2 || len(parts[0]) < 1 || len(parts[0]) > 63 {
		return "", ErrDomain
	}
	switch parts[1] {
	case "com", "net", "org":
	default:
		return "", ErrDomain
	}
	label := parts[0]
	if label[0] == '-' || label[len(label)-1] == '-' || strings.HasPrefix(label, "xn--") {
		return "", ErrDomain
	}
	// Reserved IDNA hyphen positions are excluded even when the label is ASCII.
	if len(label) >= 4 && label[2:4] == "--" {
		return "", ErrDomain
	}
	for _, r := range label {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return "", ErrDomain
		}
	}
	return name, nil
}
