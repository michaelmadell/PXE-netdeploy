package autoinstall

import (
	"crypto/rand"
	"crypto/sha512"
	"strings"
)

// SHA-512 crypt ("$6$", Ulrich Drepper's SHA-crypt specification), the
// password hash autoinstall's identity section takes (ADR 0035). Only the
// default 5000 rounds, which glibc's crypt(3) verifies without a rounds=
// field.

const (
	cryptRounds   = 5000
	cryptSaltLen  = 16
	cryptAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// HashPassword hashes a password with a new random salt.
func HashPassword(password string) string {
	b := make([]byte, cryptSaltLen)
	rand.Read(b)
	salt := make([]byte, cryptSaltLen)
	for i, c := range b {
		salt[i] = cryptAlphabet[int(c)%len(cryptAlphabet)]
	}
	return sha512Crypt([]byte(password), salt)
}

// Crypt hashes a password with the given salt (at most 16 characters
// are used).
func Crypt(password, salt string) string { return sha512Crypt([]byte(password), []byte(salt)) }

// repeatTo is b repeated to n bytes.
func repeatTo(b []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, b[:min(len(b), n-len(out))]...)
	}
	return out
}

func sha512Crypt(key, salt []byte) string {
	if len(salt) > cryptSaltLen {
		salt = salt[:cryptSaltLen]
	}
	h := sha512.New()
	h.Write(key)
	h.Write(salt)
	h.Write(key)
	b := h.Sum(nil)

	h.Reset()
	h.Write(key)
	h.Write(salt)
	h.Write(repeatTo(b, len(key)))
	for i := len(key); i > 0; i >>= 1 {
		if i&1 != 0 {
			h.Write(b)
		} else {
			h.Write(key)
		}
	}
	a := h.Sum(nil)

	h.Reset()
	for range key {
		h.Write(key)
	}
	p := repeatTo(h.Sum(nil), len(key))

	h.Reset()
	for i := 0; i < 16+int(a[0]); i++ {
		h.Write(salt)
	}
	s := repeatTo(h.Sum(nil), len(salt))

	c := a
	for r := 0; r < cryptRounds; r++ {
		h.Reset()
		if r&1 != 0 {
			h.Write(p)
		} else {
			h.Write(c)
		}
		if r%3 != 0 {
			h.Write(s)
		}
		if r%7 != 0 {
			h.Write(p)
		}
		if r&1 != 0 {
			h.Write(c)
		} else {
			h.Write(p)
		}
		c = h.Sum(nil)
	}

	var out strings.Builder
	out.WriteString("$6$")
	out.Write(salt)
	out.WriteByte('$')
	enc := func(b2, b1, b0 byte, n int) {
		w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
		for ; n > 0; n-- {
			out.WriteByte(cryptAlphabet[w&0x3f])
			w >>= 6
		}
	}
	order := [][3]int{{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48},
		{28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13},
		{56, 14, 35}, {15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41}}
	for _, o := range order {
		enc(c[o[0]], c[o[1]], c[o[2]], 4)
	}
	enc(0, 0, c[63], 2)
	return out.String()
}
