package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

const input2 = `
/Artifact <</Attached [/Top ]/BBox [50.2756 790.9277 227.5464 802.9651 ]/Subtype /Header /Type /Pagination >>BDC
BT
/CS0 cs 0  scn
/TT0 1 Tf
-0.001 Tc 0.002 Tw 9.96 -0 0 9.96 56.64 793.56 Tm
[(E)4.1 (SA U)-11.4 (N)0.7 (C)-1.8 (L)-5.9 (ASSI)-13.8 (F)4.1 (I)-13.8 (E)4.1 (D)0.7 ( )]TJ
0 Tc 0 Tw (-)Tj
8.518 0 Td
( )Tj
0.004 Tc -0.003 Tw 0.313 0 Td
[(F)9.1 (o)-5 (r)5.8 ( O)5.7 (ffic)-3.6 (ia)9.1 (l U)5.7 (s)5.3 (e)]TJ
0 Tc 0 Tw 6.072 0 Td
( )Tj
ET
q
0 0 595.32 842.04 re
W n
BT
/CS0 CS 0  SCN
0.285 w
2 Tr 9.96 -0 0 9.96 208.08 793.56 Tm
( )Tj
ET
Q
BT
9.96 -0 0 9.96 211.2 793.56 Tm
( )Tj
0.301 0 Td
( )Tj
ET
EMC
`

var operators = []string{
	"BT",
	"ET",
	"T*",
	"Tc",
	"Td",
	"Td",
	"Tf",
	"Tj",
	"TJ",
	"TL",
	"Tm",
	"Tr",
	"Ts",
	"Tw",
	"Tz",
	"w",
	"q",
	"Q",
	"BDC",
	"EMC",
	"'",
	"\"",
	"T*",
	"re",
	"W*",
	"gs",
	"n",
}

func init() {
	sort.Strings(operators)
}

type Line struct {
	Id      string
	Content []string
}

func (i Line) Text() string {
	str := strings.Join(i.Content, "")
	return strings.TrimSpace(str)
}

func main() {
	flag.Parse()
	var (
		tmp   []byte
		stack []Token
	)
	if flag.NArg() > 0 {
		for _, a := range flag.Args() {
			buf, err := os.ReadFile(a)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			tmp = append(tmp, buf...)
		}
	} else {
		tmp = []byte(input2)
	}
	var (
		r    = bytes.NewReader(tmp)
		w    bytes.Buffer
		last string
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
			if last == "" && w.Len() > 0 {
				w.WriteByte('\n')
			}
			if s = strings.TrimSpace(s); len(s) > 0 {
				w.WriteString(s)
			}
		}
		if tok.IsMatrix() || tok.Literal == "Td" || tok.Literal == "TD" {
			offset := stack[len(stack)-1].Literal
			if last != offset && offset != "0" {
				w.WriteByte('\n')
			}
			last = offset
		}
		stack = stack[:0]
	}
	fmt.Println(w.String())
}

func readToken(r *bytes.Reader) Token {
	var k Token
	switch b, _ := r.ReadByte(); {
	case b == '/':
		k = readName(r)
	case isLetter(b) || isQuote(b):
		k = readIdent(r)
	case b == '[':
		k.Type = BegArr
	case b == ']':
		k.Type = EndArr
	case b == '(':
		k = readString(r)
	case b == '>':
		r.ReadByte()
		k.Type = EndDict
	case b == '<':
		b, _ = r.ReadByte()
		if b == '<' {
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

func readIdent(r *bytes.Reader) Token {
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

func readName(r *bytes.Reader) Token {
	var str bytes.Buffer
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if !isLetter(b) && !isDigit(b) && b != '_' {
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

func readString(r *bytes.Reader) Token {
	var str bytes.Buffer
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if b == ')' {
			break
		}
		if b == '\\' {
			b, _ = r.ReadByte()
		}
		str.WriteByte(b)
	}
	return Token{
		Literal: str.String(),
		Type:    String,
	}
}

func readHex(r *bytes.Reader) Token {
	var str bytes.Buffer
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if b == '>' {
			break
		} else if isBlank(b) {
			skipBlank(r)
			continue
		} else if isHex(b) {
			c1, _ := fromHexChar(b)
			b, _ = r.ReadByte()
			if b == '>' || isBlank(b) {
				b = '0'
				r.UnreadByte()
			}
			c2, _ := fromHexChar(b)
			str.WriteByte((c1 << 4) | c2)
		}
	}
	return Token{
		Literal: str.String(),
		Type:    String,
	}
}

func readNumber(r *bytes.Reader) Token {
	r.UnreadByte()

	var str bytes.Buffer
	b, _ := r.ReadByte()
	str.WriteByte(b)
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if !isDigit(b) && b != '.' {
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

func skipBlank(r *bytes.Reader) {
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if !isBlank(b) {
			r.UnreadByte()
			break
		}
	}
}

func isNumber(b byte) bool {
	return b == '-' || isDigit(b)
}

func isDigit(b byte) bool {
	return (b >= '0' && b <= '9')
}

func isHex(b byte) bool {
	return isDigit(b) || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isBlank(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

func isQuote(b byte) bool {
	return b == '\'' || b == '"'
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
