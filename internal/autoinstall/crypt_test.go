package autoinstall

import (
	"strings"
	"testing"
)

// The specification's test vector with the default rounds, and one from
// OpenSSL 3 (openssl passwd -6) with a 16-character salt and a long key.
func TestSHA512Crypt(t *testing.T) {
	for _, c := range []struct{ salt, key, want string }{
		{"saltstring", "Hello world!", "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		{"aB3./xyz0123QRst", "correct horse battery staple", "$6$aB3./xyz0123QRst$2tCo2FAJ1UMpyhAJ4zyGIBfMtk5FqHfR7ol0P3fDcgnsGvgER1UNzSV7eFQpI2LDuWaFBXaLOo1IkYiDQ0t2D0"},
	} {
		if got := sha512Crypt([]byte(c.key), []byte(c.salt)); got != c.want {
			t.Errorf("%q/%q: %s", c.salt, c.key, got)
		}
	}
	h := HashPassword("x")
	if !strings.HasPrefix(h, "$6$") || len(h) != 3+16+1+86 || h == HashPassword("x") {
		t.Errorf("HashPassword %q", h)
	}
}
