package signer

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/foxboron/go-uefi/authenticode"
	"github.com/foxboron/go-uefi/pkcs7"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/udf"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/wim"
)

func TestEmbeddedCAs(t *testing.T) {
	want := map[Signer]string{
		WindowsPCA2011:    "580a6f4cc4e4b669b9ebdc1b2b3e087b80d0678d",
		UEFICA2011:        "46def63b5ce61cf8ba0de2e6639c1019d0ed14f3",
		WindowsUEFICA2023: "45a0fa32604773c82433c3b7d59e7466b3ac0c67",
		UEFICA2023:        "b5eeb4a6706048073f0ed296e7f580a790b59eaa",
	}
	if len(Default().cas) != len(want) {
		t.Fatalf("%d CAs", len(Default().cas))
	}
	for _, ca := range Default().cas {
		sum := sha1.Sum(ca.Cert.Raw)
		if got := hex.EncodeToString(sum[:]); got != want[ca.Signer] {
			t.Errorf("%s: thumbprint %s, want %s (%s)", ca.Signer, got, want[ca.Signer], ca.Cert.Subject)
		}
	}
}

// makePE builds a minimal PE32+ EFI application with one section.
func makePE(t *testing.T) []byte {
	t.Helper()
	const lfanew, hdrSize, secSize = 0x80, 0x400, 0x200
	b := make([]byte, hdrSize+secSize)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], lfanew)
	p := b[lfanew:]
	copy(p, "PE\x00\x00")
	coff := p[4:]
	binary.LittleEndian.PutUint16(coff[0:], 0x8664) // machine: x64
	binary.LittleEndian.PutUint16(coff[2:], 1)      // sections
	binary.LittleEndian.PutUint16(coff[16:], 240)   // optional header size
	binary.LittleEndian.PutUint16(coff[18:], 0x22)  // executable, large address aware
	opt := coff[20:]
	binary.LittleEndian.PutUint16(opt[0:], 0x20b) // PE32+
	binary.LittleEndian.PutUint32(opt[16:], 0x1000)
	binary.LittleEndian.PutUint64(opt[24:], 0x10000000) // image base
	binary.LittleEndian.PutUint32(opt[32:], 0x1000)     // section alignment
	binary.LittleEndian.PutUint32(opt[36:], 0x200)      // file alignment
	binary.LittleEndian.PutUint32(opt[56:], 0x2000)     // size of image
	binary.LittleEndian.PutUint32(opt[60:], hdrSize)    // size of headers
	binary.LittleEndian.PutUint16(opt[68:], 10)         // subsystem: EFI application
	binary.LittleEndian.PutUint32(opt[108:], 16)        // data directories
	sec := opt[240:]
	copy(sec, ".text")
	binary.LittleEndian.PutUint32(sec[8:], secSize)
	binary.LittleEndian.PutUint32(sec[12:], 0x1000)
	binary.LittleEndian.PutUint32(sec[16:], secSize)
	binary.LittleEndian.PutUint32(sec[20:], hdrSize)
	binary.LittleEndian.PutUint32(sec[36:], 0x60000020)
	for i := range secSize {
		b[hdrSize+i] = byte(i)
	}
	if _, err := pe.NewFile(bytes.NewReader(b)); err != nil {
		t.Fatalf("test PE doesn't parse: %v", err)
	}
	return b
}

type keyCert struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
}

// issue makes a certificate signed by parent (self-signed if nil).
func issue(t *testing.T, cn string, parent *keyCert, ca bool, notAfter time.Time) *keyCert {
	t.Helper()
	// RSA, as Microsoft's signatures are (go-uefi's PKCS#7 verifier is
	// RSA-only).
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: notAfter,
		IsCA: ca, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	signerCert, signerKey := tmpl, key
	if parent != nil {
		signerCert, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &keyCert{key, cert}
}

func sign(t *testing.T, img []byte, leaf *keyCert, extra ...*x509.Certificate) []byte {
	t.Helper()
	p, err := authenticode.Parse(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Sign(leaf.key, leaf.cert, pkcs7.WithAdditionalCerts(extra)); err != nil {
		t.Fatal(err)
	}
	return p.Bytes()
}

func classify(t *testing.T, c *Classifier, b []byte) Result {
	t.Helper()
	r, err := c.Classify(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClassify(t *testing.T) {
	expired := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // like the 2011 CAs
	root := issue(t, "Test Root", nil, true, time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC))
	ca := issue(t, "Test Windows CA", root, true, expired)
	other := issue(t, "Unrelated CA", nil, true, time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC))
	c := New(CA{Signer: WindowsPCA2011, Cert: ca.cert})

	img := makePE(t)
	if r := classify(t, c, img); r.Signer != Unsigned {
		t.Errorf("unsigned: %+v", r)
	}

	// Signed by a leaf under the (expired) CA, chain carried in the signature.
	leaf := issue(t, "Test Windows", ca, false, expired)
	signed := sign(t, img, leaf, ca.cert)
	if r := classify(t, c, signed); r.Signer != WindowsPCA2011 || r.Subject != "CN=Test Windows" {
		t.Errorf("signed: %+v", r)
	}
	// Without the intermediate in the bag it still chains (the CA is the root).
	if r := classify(t, c, sign(t, img, leaf)); r.Signer != WindowsPCA2011 {
		t.Errorf("signed, no bag: %+v", r)
	}

	// Tampered after signing: the digest no longer matches.
	tampered := bytes.Clone(signed)
	tampered[0x400+10] ^= 1
	if r := classify(t, c, tampered); r.Signer != Unknown {
		t.Errorf("tampered: %+v", r)
	}

	// Validly signed, but by a CA we don't trust.
	if r := classify(t, c, sign(t, img, issue(t, "Someone", other, false, expired))); r.Signer != Unknown || r.Subject != "CN=Someone" {
		t.Errorf("unknown CA: %+v", r)
	}

	// Not a PE at all.
	for _, junk := range [][]byte{nil, []byte("hello"), append([]byte("MZ"), make([]byte, 100)...)} {
		if _, err := c.Classify(bytes.NewReader(junk), int64(len(junk))); !errors.Is(err, ErrNotPE) {
			t.Errorf("junk %q: %v", junk[:min(len(junk), 4)], err)
		}
	}
}

// TestRealMedia classifies the boot managers on real media when
// NETDEPLOY_TEST_ISO is set: Phase 1 found the media's bootx64.efi signed
// under PCA 2011 and boot.wim's bootmgfw_EX.efi under the 2023 CA.
func TestRealMedia(t *testing.T) {
	p := os.Getenv("NETDEPLOY_TEST_ISO")
	if p == "" {
		t.Skip("set NETDEPLOY_TEST_ISO to a Windows ISO")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	u, err := udf.Open(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	b, err := fs.ReadFile(u, "efi/boot/bootx64.efi")
	if err != nil {
		t.Fatal(err)
	}
	r := classify(t, Default(), b)
	t.Logf("efi/boot/bootx64.efi: %s (%s)", r.Signer, r.Subject)
	if r.Signer != WindowsPCA2011 {
		t.Errorf("bootx64.efi: %s", r.Signer)
	}
	bw, _ := u.Open("sources/boot.wim")
	fi, _ := bw.Stat()
	wr, err := wim.Open(bw.(io.ReaderAt), fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Windows/Boot/EFI/bootmgfw.efi", "Windows/Boot/EFI_EX/bootmgfw_EX.efi"} {
		b, err := wr.ReadFile(2, name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		r := classify(t, Default(), b)
		t.Logf("boot.wim:2/%s: %s (%s)", name, r.Signer, r.Subject)
	}
	if b, err := wr.ReadFile(2, "Windows/Boot/EFI_EX/bootmgfw_EX.efi"); err == nil {
		if r := classify(t, Default(), b); r.Signer != WindowsUEFICA2023 {
			t.Errorf("bootmgfw_EX.efi: %s", r.Signer)
		}
	}
}
