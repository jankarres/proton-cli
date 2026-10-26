package meet

import (
	"net/url"
	"strings"

	"github.com/roman-16/proton-cli/internal/errs"
)

// The parts of a meeting link, as the web app writes and parseMeetingLink in
// @proton/shared/lib/meet/parseMeetingLink reads them.
const (
	linkHost       = "meet.proton.me"
	linkPathPrefix = "/join/id-"
	linkFragment   = "pwd-"
	// linkNameLength is MEETING_LINK_ID_LENGTH.
	linkNameLength = 10
)

// Link is the address somebody joins a meeting by: its link name in the path,
// and its password in the fragment, which a browser never sends to a server.
func Link(name, password string) string {
	return "https://" + linkHost + linkPathPrefix + name + "#" + linkFragment + password
}

// ParseLink takes a meeting link apart into its link name and its password.
//
// It takes the link as the web app writes it, the same without its scheme as a
// link copied out of an address bar often is, and the bare link name, which
// carries no password. A link without a fragment has no password either, but a
// fragment that is there and is not one is refused rather than read as none,
// since a password cut off on the way would otherwise surface much later as a
// meeting that will not open. A password is twelve characters of the web app's
// alphabet, as isUrlPasswordValid in packages/meet/utils checks: a custom
// password a host adds never travels in the link.
func ParseLink(s string) (name, password string, err error) {
	s = strings.TrimSpace(s)
	if isLinkName(s) {
		return s, "", nil
	}
	notALink := errs.Problemf("That is not a Proton Meet link.").
		Hint("pass the whole link, or the ten-character ID after id-")
	raw := s
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, perr := url.Parse(raw)
	if perr != nil || !strings.EqualFold(u.Hostname(), linkHost) {
		return "", "", notALink
	}
	name, ok := strings.CutPrefix(u.Path, linkPathPrefix)
	if !ok || !isLinkName(name) {
		return "", "", notALink
	}
	if !strings.Contains(raw, "#") {
		return name, "", nil
	}
	password, ok = strings.CutPrefix(u.Fragment, linkFragment)
	if !ok || !inAlphabet(password, passwordLength) {
		return "", "", errs.Problemf("The password in that Proton Meet link is missing or cut off.").
			Hint("copy the whole link, including the part after #pwd-")
	}
	return name, password, nil
}

// isLinkName says whether s could be a meeting's link name: ten letters and
// digits, as the web app checks.
func isLinkName(s string) bool {
	return inAlphabet(s, linkNameLength)
}

// inAlphabet says whether s is exactly n characters of the web app's alphabet,
// which link names and link passwords share.
func inAlphabet(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(passwordAlphabet, r) {
			return false
		}
	}
	return true
}
