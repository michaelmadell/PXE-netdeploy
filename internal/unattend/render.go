package unattend

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"strings"
	"unicode/utf16"
)

// Credential is a domain-credential secret's value.
type Credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// SplitAccount splits DOMAIN\user or user@domain; a bare user name takes
// def as its domain.
func SplitAccount(username, def string) (domain, user string) {
	if i := strings.IndexByte(username, '\\'); i >= 0 {
		return username[:i], username[i+1:]
	}
	if i := strings.LastIndexByte(username, '@'); i >= 0 {
		return username[i+1:], username[:i]
	}
	return def, username
}

// encodePassword is unattend's password encoding (PlainText false):
// base64 of UTF-16LE(password + "Password"). It only hides the value from
// a casual look.
func encodePassword(p string) string {
	u := utf16.Encode([]rune(p + "Password"))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}
