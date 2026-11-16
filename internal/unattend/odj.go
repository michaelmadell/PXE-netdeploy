package unattend

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"
)

// ErrNotODJBlob: not a djoin /provision /savefile file.
var ErrNotODJBlob = errors.New("not an offline domain join blob (djoin /provision /savefile)")

// ODJAccountData returns the base64 text of a djoin /provision /savefile
// file, as UnattendedJoin's Provisioning/AccountData takes it. The file is
// usually UTF-16LE (with or without a BOM and a trailing NUL); UTF-8 is
// accepted too (/printblob output saved to a file).
func ODJAccountData(file []byte) (string, error) {
	text := string(file)
	switch {
	case len(file) >= 2 && file[0] == 0xFF && file[1] == 0xFE:
		text = decodeUTF16(file[2:])
	case len(file) >= 2 && file[1] == 0 && file[0] != 0:
		text = decodeUTF16(file)
	}
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.Map(func(r rune) rune {
		switch r {
		case 0, ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, text)
	if len(text) < 64 {
		return "", ErrNotODJBlob
	}
	if _, err := base64.StdEncoding.DecodeString(text); err != nil {
		return "", ErrNotODJBlob
	}
	return text, nil
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}
