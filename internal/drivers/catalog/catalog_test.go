package catalog

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/michaelmadell/pxe-netdeploy/internal/drivers/inf"
	"github.com/smallstep/pkcs7"
	"golang.org/x/crypto/cryptobyte"
	cbasn1 "golang.org/x/crypto/cryptobyte/asn1"
)

func TestEmbeddedRoots(t *testing.T) {
	want := map[string]bool{
		"3b1efd3a66ea28b16697394703a72ca340a05bd5": true, // 2010
		"8f43288ad272f3103b6fb1428485ea3014c0bcfe": true, // 2011
		"cdd4eeae6000ac7f40c3802c171e30148030c072": true, // 2001
		"a43489159a520f0d93d032ccaf37e7fe20a8b419": true, // Root Authority
	}
	if len(Default().roots) != len(want) {
		t.Fatalf("%d roots", len(Default().roots))
	}
	for _, r := range Default().roots {
		sum := sha1.Sum(r.Raw)
		if !want[hex.EncodeToString(sum[:])] {
			t.Errorf("unexpected root %s (%x)", r.Subject, sum)
		}
	}
}

type keyCert struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
}

func issue(t *testing.T, cn string, parent *keyCert, ca bool) *keyCert {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: ca, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	pc, pk := tmpl, key
	if parent != nil {
		pc, pk = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, pc, &key.PublicKey, pk)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &keyCert{key, c}
}

// ctl builds a v2-style certificate trust list listing digests, plus one
// v1-style member identified only by its UTF-16 hex tag.
func ctl(digests [][]byte, v1tag []byte) []byte {
	var b cryptobyte.Builder
	b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
		b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
			b.AddASN1ObjectIdentifier(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 12, 1, 1})
		})
		b.AddASN1OctetString([]byte("list"))
		b.AddASN1UTCTime(time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC))
		b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
			b.AddASN1ObjectIdentifier(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 12, 1, 3})
		})
		b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
			for _, d := range digests {
				b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
					b.AddASN1OctetString(d)
					b.AddASN1(cbasn1.SET, func(b *cryptobyte.Builder) {
						b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
							b.AddASN1ObjectIdentifier(oidIndirectData)
							b.AddASN1(cbasn1.SET, func(b *cryptobyte.Builder) {
								b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
									b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
										b.AddASN1ObjectIdentifier(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 2, 1, 25})
									})
									b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
										b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) {
											b.AddASN1ObjectIdentifier(asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1})
										})
										b.AddASN1OctetString(d)
									})
								})
							})
						})
					})
				})
			}
			b.AddASN1(cbasn1.SEQUENCE, func(b *cryptobyte.Builder) { b.AddASN1OctetString(v1tag) })
		})
	})
	return b.BytesOrPanic()
}

func sign(t *testing.T, content []byte, signer *keyCert, extra ...*x509.Certificate) []byte {
	t.Helper()
	sd, err := pkcs7.NewSignedData(content)
	if err != nil {
		t.Fatal(err)
	}
	// smallstep/pkcs7 signs with the "data" content type only; real
	// catalogs say CTL. Parse reads the content either way.
	if err := sd.AddSigner(signer.cert, signer.key, pkcs7.SignerInfoConfig{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range extra {
		sd.AddCertificate(c)
	}
	der, err := sd.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func utf16hex(sum []byte) []byte {
	var out []byte
	for _, u := range utf16.Encode([]rune(hex.EncodeToString(sum))) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func TestVerifyAndMembership(t *testing.T) {
	root := issue(t, "Test Microsoft Root", nil, true)
	pca := issue(t, "Test Third Party Component CA", root, true)
	whql := issue(t, "Test Hardware Compatibility Publisher", pca, false)

	infBody := []byte("[Version]\nSignature=\"$Windows NT$\"\n")
	sys := bytes.Repeat([]byte("driver"), 1000)
	v1 := []byte("old style member")
	s256 := sha256.Sum256(infBody)
	s1 := sha1.Sum(sys)
	v1sum := sha1.Sum(v1)
	der := sign(t, ctl([][]byte{s256[:], s1[:]}, utf16hex(v1sum[:])), whql, pca.cert)

	c, err := Parse(der)
	if err != nil {
		t.Fatal(err)
	}
	if r := New(root.cert).Verify(c); r.Signature != WHQL || r.Signer != "CN=Test Hardware Compatibility Publisher" || r.Detail != "" {
		t.Errorf("trusted root: %+v", r)
	}
	if r := New().Verify(c); r.Signature != Vendor || r.Detail == "" {
		t.Errorf("unknown root: %+v", r)
	}
	for name, body := range map[string][]byte{"inf (sha256)": infBody, "sys (sha1)": sys, "v1 tag": v1} {
		if ok, err := c.Contains(bytes.NewReader(body), int64(len(body))); !ok || err != nil {
			t.Errorf("%s not covered: %v", name, err)
		}
	}
	other := []byte("a swapped file")
	if ok, _ := c.Contains(bytes.NewReader(other), int64(len(other))); ok {
		t.Error("a file not in the catalog is covered")
	}

	// A tampered signature doesn't verify.
	bad := bytes.Clone(der)
	bad[len(bad)-20] ^= 0xff
	if c2, err := Parse(bad); err == nil {
		if r := New(root.cert).Verify(c2); r.Signature != Unsigned {
			t.Errorf("tampered: %+v", r)
		}
	}
	// Not a catalog at all.
	for _, junk := range [][]byte{nil, []byte("hello"), bytes.Repeat([]byte{0x30}, 100)} {
		if _, err := Parse(junk); err == nil {
			t.Errorf("junk %q parsed", junk[:min(4, len(junk))])
		}
	}
}

// TestDriverStore verifies every catalog in a Windows DriverStore and checks
// that each INF, and the files it lists that are present, are covered.
func TestDriverStore(t *testing.T) {
	root := os.Getenv("NETDEPLOY_TEST_DRIVERSTORE")
	if root == "" {
		t.Skip("set NETDEPLOY_TEST_DRIVERSTORE to a DriverStore FileRepository")
	}
	infs, _ := filepath.Glob(filepath.Join(root, "*", "*.inf"))
	results := map[string]int{}
	var checked, covered int
	var uncovered []string
	for _, p := range infs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		d, _, err := inf.Read(b)
		if err != nil || d.Catalogs["x64"] == "" {
			continue
		}
		dir := filepath.Dir(p)
		cb, err := os.ReadFile(filepath.Join(dir, d.Catalogs["x64"]))
		if err != nil {
			results["catalog missing"]++
			continue
		}
		c, err := Parse(cb)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		r := Default().Verify(c)
		results[r.Signature]++
		if r.Signature == Unsigned || r.Signature == Unknown {
			t.Logf("%s: %s: %s", filepath.Base(dir), r.Signature, r.Detail)
		}
		for _, f := range append([]string{filepath.Base(p)}, d.Files...) {
			fp := filepath.Join(dir, filepath.FromSlash(f))
			fb, err := os.ReadFile(fp)
			if err != nil {
				continue // installed elsewhere
			}
			enforced := f == filepath.Base(p) || IsPE(bytes.NewReader(fb))
			if !enforced {
				continue
			}
			checked++
			if ok, _ := c.Contains(bytes.NewReader(fb), int64(len(fb))); ok {
				covered++
			} else if len(uncovered) < 10 {
				uncovered = append(uncovered, filepath.Base(dir)+"/"+f)
			}
		}
	}
	t.Logf("signatures: %v; INF and PE files covered %d of %d", results, covered, checked)
	if covered != checked {
		t.Errorf("not covered (first 10): %v", uncovered)
	}
}
