package pdf

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/encoding/unicode"
)

func init() {
	sort.Strings(operators)
}

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

func fromHexChar(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return (b - 'a') + 10, true
	case b >= 'A' && b <= 'F':
		return (b - 'A') + 10, true
	}
	return 0, false
}

var timePatterns = []string{
	"D:20060102150405-0700",
	"D:20060102150405",
	"D:20060102150405Z",
	"D:20060102",
}

func parseTime(str string) (time.Time, error) {
	var (
		when time.Time
		err  error
	)
	str = strings.ReplaceAll(str, "'", "")
	for _, pat := range timePatterns {
		when, err = time.Parse(pat, str)
		if err == nil {
			break
		}
	}
	return when, err
}

func getPageContent(body []byte) []byte {
	var (
		r     = NewReader(body)
		w     bytes.Buffer
		stack []Token
		line  string
	)
	for r.Len() > 0 {
		tok := readToken(r)
		if !tok.IsOperator() {
			stack = append(stack, tok)
			continue
		}
		if tok.IsShow() {
			var str []string
			for i := range stack {
				if stack[i].Type == String {
					str = append(str, stack[i].Literal)
				}
			}
			s := strings.Join(str, "")
			if line == "" && w.Len() > 0 {
				w.WriteByte('\n')
			}
			if s = strings.TrimSpace(s); len(s) > 0 {
				w.WriteString(s)
			}
		}
		if tok.IsMatrix() || tok.Literal == "Td" || tok.Literal == "TD" {
			offset := stack[len(stack)-1].Literal
			if line != offset && offset != "0" {
				w.WriteByte('\n')
			}
			line = offset
		}
		stack = stack[:0]
	}
	return w.Bytes()
}

func readToken(r *Reader) Token {
	var k Token
	switch b, _ := r.ReadByte(); {
	case b == slash:
		k = readName(r)
	case isLetter(b) || isQuote(b):
		k = readIdent(r)
	case b == lsquare:
		k.Type = BegArr
	case b == rsquare:
		k.Type = EndArr
	case b == lparen:
		k = readString(r)
	case b == rangle:
		r.ReadByte()
		k.Type = EndDict
	case b == langle:
		b, _ = r.ReadByte()
		if b == langle {
			k.Type = BegDict
			break
		}
		r.UnreadByte()
		k = readHex(r)
	case isBlank(b):
		skipBlank(r)
		return readToken(r)
	case isNumber(b):
		k = readNumber(r)
	default:
		k.Type = Invalid
		if b == 0 {
			k.Type = EOF
		}
	}
	return k
}

func readIdent(r *Reader) Token {
	r.UnreadByte()

	var str bytes.Buffer
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if !isLetter(b) && !isQuote(b) {
			r.UnreadByte()
			break
		}
		str.WriteByte(b)
	}
	return Token{
		Literal: str.String(),
		Type:    Ident,
	}
}

func readName(r *Reader) Token {
	var str bytes.Buffer
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if !isLetter(b) && !isDigit(b) && b != underscore {
			r.UnreadByte()
			break
		}
		str.WriteByte(b)
	}
	return Token{
		Literal: str.String(),
		Type:    Name,
	}
}

func readString(r *Reader) Token {
	var str bytes.Buffer
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if b == rparen {
			break
		}
		if b == backslash {
			b, _ = r.ReadByte()
		}
		str.WriteByte(b)
	}
	return Token{
		Literal: str.String(),
		Type:    String,
	}
}

func readHex(r *Reader) Token {
	var (
		str bytes.Buffer
		tmp bytes.Buffer
	)
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if b == rangle {
			break
		} else if isBlank(b) {
			skipBlank(r)
			continue
		} else if isHex(b) {
			tmp.WriteByte(b)
			c1, _ := fromHexChar(b)
			b, _ = r.ReadByte()
			if b == rangle || isBlank(b) {
				b = '0'
				r.UnreadByte()
			}
			tmp.WriteByte(b)
			c2, _ := fromHexChar(b)
			str.WriteByte((c1 << 4) | c2)
		}
	}

	fmt.Printf("str: %s\n", str.String())
	fmt.Printf("tmp: %s\n", tmp.String())
	return Token{
		Literal: str.String(),
		Type:    String,
	}
}

func readNumber(r *Reader) Token {
	r.UnreadByte()

	var str bytes.Buffer
	b, _ := r.ReadByte()
	str.WriteByte(b)
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if !isDigit(b) && b != dot {
			r.UnreadByte()
			break
		}
		str.WriteByte(b)
	}
	return Token{
		Literal: str.String(),
		Type:    Number,
	}
}

func skipBlank(r *Reader) {
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if !isBlank(b) {
			r.UnreadByte()
			break
		}
	}
}

const (
	EOF rune = -(iota + 1)
	Ident
	Name
	String
	Number
	Invalid
	BegArr
	EndArr
	BegDict
	EndDict
)

var operators = []string{
	"b",
	"B",
	"b*",
	"B*",
	"BDC",
	"BI",
	"BMC",
	"BT",
	"BX",
	"c",
	"cm",
	"CS",
	"cs",
	"d",
	"d0",
	"d1",
	"Do",
	"DP",
	"EI",
	"EMC",
	"ET",
	"EX",
	"f",
	"F",
	"f*",
	"G",
	"g",
	"gs",
	"h",
	"i",
	"ID",
	"j",
	"J",
	"K",
	"k",
	"l",
	"m",
	"M",
	"MP",
	"n",
	"q",
	"Q",
	"re",
	"RG",
	"rg",
	"ri",
	"s",
	"S",
	"SC",
	"sc",
	"SCN",
	"scn",
	"sh",
	"T*",
	"Tc",
	"Td",
	"TD",
	"Tf",
	"Tj",
	"TJ",
	"TL",
	"Tm",
	"Tr",
	"Ts",
	"Tw",
	"Tz",
	"v",
	"w",
	"W",
	"W*",
	"y",
	"'",
	"\"",
}

type Token struct {
	Literal string
	Type    rune
}

func (t Token) IsShow() bool {
	return t.Type == Ident && (t.Literal == "Tj" || t.Literal == "TJ")
}

func (t Token) IsMatrix() bool {
	return t.Type == Ident && t.Literal == "Tm"
}

func (t Token) IsOperator() bool {
	if t.Type != Ident {
		return false
	}
	i := sort.SearchStrings(operators, t.Literal)
	return i < len(operators) && operators[i] == t.Literal
}

func (t Token) isValid() bool {
	return t.Type != 0
}

func (t Token) String() string {
	var prefix string
	switch t.Type {
	case EOF:
		return "<eof>"
	case Ident:
		prefix = "ident"
	case Name:
		prefix = "name"
	case String:
		prefix = "string"
	case Number:
		prefix = "number"
	case BegDict:
		return "<begin(dict)>"
	case EndDict:
		return "<end(dict)>"
	case BegArr:
		return "<begin(array)>"
	case EndArr:
		return "<end(array)>"
	case Invalid:
		return "<invalid>"
	default:
		return fmt.Sprintf("<unknown(%d)>", t.Type)
	}
	return fmt.Sprintf("<%s(%s)>", prefix, t.Literal)
}

const (
	nl         = '\n'
	cr         = '\r'
	percent    = '%'
	space      = ' '
	tab        = '\t'
	formfeed   = '\f'
	backspace  = '\b'
	langle     = '<'
	rangle     = '>'
	lsquare    = '['
	rsquare    = ']'
	lparen     = '('
	rparen     = ')'
	pound      = '#'
	slash      = '/'
	minus      = '-'
	plus       = '+'
	dot        = '.'
	backslash  = '\\'
	squote     = '\''
	dquote     = '"'
	underscore = '_'
)

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isNumber(b byte) bool {
	return isDigit(b) || isSign(b)
}

func isSign(b byte) bool {
	return b == minus || b == plus
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isHex(b byte) bool {
	return isDigit(b) || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'F')
}

func isSpace(b byte) bool {
	return b == space || b == tab
}

func isBlank(b byte) bool {
	return isSpace(b) || b == cr || b == nl
}

func isQuote(b byte) bool {
	return b == squote || b == dquote
}
