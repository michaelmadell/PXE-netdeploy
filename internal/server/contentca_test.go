package server

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/contentca"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
)

func TestContentCAEndpoint(t *testing.T) {
	dir := t.TempDir()
	ca := contentca.New(contentca.Options{DataDir: dir, Ring: secrets.NewKeys(dir).Ring})
	if err := ca.Load(); err != nil {
		t.Fatal(err)
	}
	s := New("test", Deps{CA: ca})
	ctx := context.Background()
	res, _ := s.GetContentCaCertificate(ctx, api.GetContentCaCertificateRequestObject{})
	if p, ok := res.(api.GetContentCaCertificate404ApplicationProblemPlusJSONResponse); !ok || p.Code != "content_https_not_available" {
		t.Fatalf("before the CA: %#v", res)
	}
	if err := ca.Ensure(); err != nil {
		t.Fatal(err)
	}
	res, _ = s.GetContentCaCertificate(ctx, api.GetContentCaCertificateRequestObject{})
	pr, ok := res.(api.GetContentCaCertificate200ApplicationxPemFileResponse)
	if !ok {
		t.Fatalf("PEM: %#v", res)
	}
	b, _ := io.ReadAll(pr.Body)
	if blk, _ := pem.Decode(b); blk == nil || !bytes.Equal(blk.Bytes, ca.CA().Raw) {
		t.Error("PEM isn't the CA")
	}
	res, _ = s.GetContentCaCertificate(context.WithValue(ctx, ctxAccept, "application/pkix-cert"), api.GetContentCaCertificateRequestObject{})
	dr, ok := res.(api.GetContentCaCertificate200ApplicationpkixCertResponse)
	if !ok {
		t.Fatalf("DER: %#v", res)
	}
	b, _ = io.ReadAll(dr.Body)
	if !bytes.Equal(b, ca.CA().Raw) {
		t.Error("DER isn't the CA")
	}
}

func TestContentCAWarning(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ca := func(days int) *x509.Certificate {
		return &x509.Certificate{NotAfter: now.Add(time.Duration(days) * 24 * time.Hour)}
	}
	if w := contentCAWarning(ca(366), now); w != nil {
		t.Errorf("366 days: %+v", w)
	}
	if w := contentCAWarning(ca(100), now); w == nil || w.Code != "content_ca_expiring" || !strings.Contains(w.Message, "in 100 days") {
		t.Errorf("100 days: %+v", w)
	}
	if w := contentCAWarning(ca(-1), now); w == nil || !strings.Contains(w.Message, "expired") {
		t.Errorf("expired: %+v", w)
	}
	if contentCAWarning(nil, now) != nil {
		t.Error("nil")
	}
}
