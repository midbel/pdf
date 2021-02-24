package pdf

import (
	"crypto/md5"
	"crypto/rc4"
	"strings"

	"golang.org/x/text/encoding/unicode"
)

const MaxKeyLength = 16

var padding = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

var (
	encbe = unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder()
	encle = unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()
)

func convertString(str string) string {
	if strings.HasPrefix(str, "\xfe\xff") {
		str, _ = encbe.String(str)
	} else if strings.HasPrefix(str, "\xff\xfe") {
		str, _ = encle.String(str)
	}
	return str
}

func decryptBytes(key, str []byte) []byte {
	if len(key) == 0 {
		return str
	}
	ciph, err := rc4.NewCipher(key)
	if err != nil {
		return nil
	}
	ciph.XORKeyStream(str, str)
	return str
}

func decryptString(key []byte, str string) string {
	s := decryptBytes(key, []byte(str))
	return string(s)
}

func getEncryptionKey(key []byte, oid, rev int) []byte {
	if len(key) == 0 {
		return nil
	}
	decrypt := make([]byte, len(key))
	copy(decrypt, key)

	decrypt = append(decrypt, byte(oid), byte(oid>>8), byte(oid>>16))
	decrypt = append(decrypt, byte(rev), byte(rev>>8))

	var (
		sum  = md5.Sum(decrypt)
		size = len(decrypt)
	)
	if size > MaxKeyLength {
		size = MaxKeyLength
	}
	return sum[:size]
}
