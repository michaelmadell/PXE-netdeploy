// Package catalog verifies Windows driver catalogs (.cat) (ADR 0019): the
// PKCS#7 signature, who signed it (chained to Microsoft's root CAs, compiled
// in, ignoring validity periods), and whether a file is a member: catalogs
// list the hash of every file they cover, a flat SHA-1/SHA-256 or, for PE
// files, the Authenticode hash.
package catalog

import (
	"crypto"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"embed"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/foxboron/go-uefi/authenticode"
	"github.com/smallstep/pkcs7"
	"golang.org/x/crypto/cryptobyte"
	cbasn1 "golang.org/x/crypto/cryptobyte/asn1"
)

// Signatures (api DriverSignature).
const (
	WHQL     = "whql"
	Vendor   = "vendor"
	Unsigned = "unsigned"
	Unknown  = "unknown"
)

var (
	oidCTL           = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 10, 1}
	oidIndirectData  = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 2, 1, 4}
	errNotCatalog    = errors.New("catalog: not a catalog (PKCS#7 CTL)")
	maxCatalogBytes  = 64 << 20
	maxMemberEntries = 1 << 20
)

//go:embed roots/*.crt
var rootFiles embed.FS

// Verifier checks catalogs against a set of Microsoft roots.
type Verifier struct {
	roots []*x509.Certificate
}

// New returns a Verifier trusting roots as Microsoft (tests); Default
// uses the compiled-in Microsoft roots.
func New(roots ...*x509.Certificate) *Verifier { return &Verifier{roots: roots} }

// Default returns the Verifier for Microsoft's driver-signing roots.
func Default() *Verifier { return defaultVerifier }

var defaultVerifier = func() *Verifier {
	ents, err := rootFiles.ReadDir("roots")
	if err != nil {
		panic(err)
	}
	var roots []*x509.Certificate
	for _, e := range ents {
		b, err := rootFiles.ReadFile("roots/" + e.Name())
		if err != nil {
			panic(err)
		}
		c, err := x509.ParseCertificate(b)
		if err != nil {
			panic(fmt.Sprintf("catalog: embedded %s: %v", e.Name(), err))
		}
		roots = append(roots, c)
	}
	return New(roots...)
}()

// Catalog is a parsed catalog.
type Catalog struct {
	p7      *pkcs7.PKCS7
	members map[string]bool // lower-case hex digests (and v1 hex tags)
}

// Parse reads a catalog. It fails only if the data isn't a catalog at all;
// signature problems come from Verify.
func Parse(b []byte) (*Catalog, error) {
	if len(b) > maxCatalogBytes {
		return nil, fmt.Errorf("catalog: %d bytes is too large", len(b))
	}
	p7, err := parsePKCS7(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotCatalog, err)
	}
	members, err := parseCTL(p7.Content)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNotCatalog, err)
	}
	return &Catalog{p7: p7, members: members}, nil
}

func parsePKCS7(b []byte) (p7 *pkcs7.PKCS7, err error) {
	defer func() {
		if r := recover(); r != nil {
			p7, err = nil, fmt.Errorf("malformed: %v", r)
		}
	}()
	return pkcs7.Parse(b)
}

// Result is a catalog's signature classification.
type Result struct {
	Signature string // WHQL, Vendor, Unsigned or Unknown
	Signer    string // the signing certificate's subject, if any
	Detail    string // why, when not WHQL
}

// Verify checks the signature and classifies the signer.
func (v *Verifier) Verify(c *Catalog) (res Result) {
	defer func() {
		if r := recover(); r != nil {
			res = Result{Signature: Unknown, Detail: fmt.Sprintf("catalog couldn't be evaluated: %v", r)}
		}
	}()
	signer := c.p7.GetOnlySigner()
	if signer != nil {
		res.Signer = signer.Subject.String()
	}
	if err := c.p7.Verify(); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "unsupported") || strings.Contains(msg, "not implemented") {
			res.Signature, res.Detail = Unknown, "signature can't be checked here: "+msg
			return res
		}
		res.Signature, res.Detail = Unsigned, "catalog signature doesn't verify: "+msg
		return res
	}
	if signer == nil {
		res.Signature, res.Detail = Unknown, "catalog has more than one signer"
		return res
	}
	if v.chainsToMicrosoft(signer, c.p7.Certificates) {
		res.Signature = WHQL
		return res
	}
	res.Signature, res.Detail = Vendor, fmt.Sprintf("signed by %s (%s), not Microsoft", signer.Subject.CommonName, signer.Issuer.CommonName)
	return res
}

// chainsToMicrosoft checks signer against the roots, at times when the
// certificates were valid (Windows accepts timestamped signatures from
// expired certificates; firmware-style "now" checks would reject them).
func (v *Verifier) chainsToMicrosoft(signer *x509.Certificate, bag []*x509.Certificate) bool {
	inter := x509.NewCertPool()
	for _, c := range bag {
		if c != signer {
			inter.AddCert(c)
		}
	}
	roots := x509.NewCertPool()
	for _, r := range v.roots {
		roots.AddCert(r)
	}
	for _, at := range []time.Time{signer.NotBefore.Add(time.Minute), signer.NotAfter.Add(-time.Minute)} {
		if _, err := signer.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: at,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err == nil {
			return true
		}
	}
	return false
}

// IsPE reports whether the data starts like a PE file (MZ). Coverage is
// enforced for the INF and PE files; other files (e.g. signed CABs, listed
// with a CAB-specific hash) only give a warning when not found.
func IsPE(r io.ReaderAt) bool {
	head := make([]byte, 2)
	_, err := r.ReadAt(head, 0)
	return err == nil && string(head) == "MZ"
}

// Members is the number of hashes the catalog lists.
func (c *Catalog) Members() int { return len(c.members) }

// Contains reports whether the file (size bytes) is covered: its flat
// SHA-1 or SHA-256, or for a PE file its Authenticode SHA-1 or SHA-256, is
// listed.
func (c *Catalog) Contains(r io.ReaderAt, size int64) (bool, error) {
	h1, h256 := sha1.New(), sha256.New()
	if _, err := io.Copy(io.MultiWriter(h1, h256), io.NewSectionReader(r, 0, size)); err != nil {
		return false, err
	}
	if c.members[hex.EncodeToString(h1.Sum(nil))] || c.members[hex.EncodeToString(h256.Sum(nil))] {
		return true, nil
	}
	head := make([]byte, 2)
	if _, err := r.ReadAt(head, 0); err != nil || string(head) != "MZ" {
		return false, nil
	}
	for _, alg := range []crypto.Hash{crypto.SHA256, crypto.SHA1} {
		if sum, ok := peHash(r, size, alg); ok && c.members[hex.EncodeToString(sum)] {
			return true, nil
		}
	}
	return false, nil
}

func peHash(r io.ReaderAt, size int64, alg crypto.Hash) (sum []byte, ok bool) {
	defer func() {
		if recover() != nil {
			sum, ok = nil, false
		}
	}()
	pe, err := authenticode.Parse(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, false
	}
	return pe.Hash(alg), true
}

// parseCTL reads the certificate trust list a catalog signs and collects
// its members' digests (from each member's SpcIndirectDataContent) and v1
// member tags (the hex SHA-1 as a UTF-16 string). PKCS#7 libraries hand
// the content back with or without the CTL's outer SEQUENCE, so both are
// tried.
func parseCTL(content []byte) (map[string]bool, error) {
	s := cryptobyte.String(content)
	var wrapped cryptobyte.String
	if s.ReadASN1(&wrapped, cbasn1.SEQUENCE) && s.Empty() {
		if m, err := parseCTLBody(wrapped); err == nil {
			return m, nil
		}
	}
	return parseCTLBody(cryptobyte.String(content))
}

func parseCTLBody(ctl cryptobyte.String) (map[string]bool, error) {
	var usage cryptobyte.String
	if !ctl.ReadASN1(&usage, cbasn1.SEQUENCE) {
		return nil, errors.New("no subject usage")
	}
	var first asn1.ObjectIdentifier
	if !usage.ReadASN1ObjectIdentifier(&first) {
		return nil, errors.New("subject usage is not a list of OIDs")
	}
	ctl.SkipOptionalASN1(cbasn1.OCTET_STRING) // listIdentifier
	ctl.SkipOptionalASN1(cbasn1.INTEGER)      // sequenceNumber
	if !ctl.SkipASN1(cbasn1.UTCTime) && !ctl.SkipASN1(cbasn1.GeneralizedTime) {
		return nil, errors.New("no thisUpdate")
	}
	ctl.SkipOptionalASN1(cbasn1.UTCTime)
	ctl.SkipOptionalASN1(cbasn1.GeneralizedTime)
	if !ctl.SkipASN1(cbasn1.SEQUENCE) { // subjectAlgorithm
		return nil, errors.New("no subject algorithm")
	}
	members := map[string]bool{}
	var subjects cryptobyte.String
	var present bool
	if !ctl.ReadOptionalASN1(&subjects, &present, cbasn1.SEQUENCE) {
		return nil, errors.New("bad trusted subjects")
	}
	for !subjects.Empty() {
		var subj, id cryptobyte.String
		if !subjects.ReadASN1(&subj, cbasn1.SEQUENCE) || !subj.ReadASN1(&id, cbasn1.OCTET_STRING) {
			return nil, errors.New("bad member")
		}
		if tag := utf16Hex(id); tag != "" {
			members[tag] = true
		}
		var attrs cryptobyte.String
		var hasAttrs bool
		if !subj.ReadOptionalASN1(&attrs, &hasAttrs, cbasn1.SET) {
			return nil, errors.New("bad member attributes")
		}
		for !attrs.Empty() {
			var attr cryptobyte.String
			var oid asn1.ObjectIdentifier
			var values cryptobyte.String
			if !attrs.ReadASN1(&attr, cbasn1.SEQUENCE) || !attr.ReadASN1ObjectIdentifier(&oid) || !attr.ReadASN1(&values, cbasn1.SET) {
				return nil, errors.New("bad attribute")
			}
			if !oid.Equal(oidIndirectData) {
				continue
			}
			for !values.Empty() {
				var spc, data, digestInfo, alg, digest cryptobyte.String
				if !values.ReadASN1(&spc, cbasn1.SEQUENCE) || !spc.ReadASN1(&data, cbasn1.SEQUENCE) ||
					!spc.ReadASN1(&digestInfo, cbasn1.SEQUENCE) || !digestInfo.ReadASN1(&alg, cbasn1.SEQUENCE) ||
					!digestInfo.ReadASN1(&digest, cbasn1.OCTET_STRING) {
					return nil, errors.New("bad indirect data")
				}
				members[hex.EncodeToString(digest)] = true
			}
		}
		if len(members) > maxMemberEntries {
			return nil, errors.New("too many members")
		}
	}
	return members, nil
}

// utf16Hex decodes a v1 member tag: a UTF-16LE string of hex digits.
func utf16Hex(b []byte) string {
	if len(b) < 2 || len(b)%2 != 0 {
		return ""
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		v := uint16(b[i]) | uint16(b[i+1])<<8
		if v == 0 {
			break
		}
		u = append(u, v)
	}
	s := strings.ToLower(string(utf16.Decode(u)))
	if _, err := hex.DecodeString(s); err != nil || len(s) < 40 {
		return ""
	}
	return s
}
