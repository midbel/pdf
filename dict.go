package pdf

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type Value interface{}

type Dict map[string]Value

func (d Dict) Linearized() bool {
	v := d.GetString("linearized")
	return v != ""
}

func (d Dict) Type() string {
	t := d.GetString("type")
	if t == "" {
		if d.Linearized() {
			t = "Linearized"
		}
	}
	return t
}

func (d Dict) Subtype() string {
	return d.GetString("subtype")
}

func (d Dict) IsEmpty() bool {
	return len(d) == 0
}

func (d Dict) IsFlate() bool {
	return d.GetString("filter") == "FlateDecode"
}

func (d Dict) IsLZW() bool {
	return d.GetString("filter") == "LZWDecode"
}

func (d Dict) Length() int64 {
	if d.Linearized() {
		return d.GetInt("l")
	}
	return d.GetInt("length")
}

func (d Dict) Has(key string) bool {
	v := d.getValue(key)
	return v != nil
}

func (d Dict) GetDict(key string) Dict {
	k, ok := d.getValue(key).(Dict)
	if !ok {
		return make(Dict)
	}
	return k
}

func (d Dict) GetBool(key string) bool {
	b, _ := d.getValue(key).(bool)
	return b
}

func (d Dict) GetBytes(key string) []byte {
	v := d.GetString(key)
	return []byte(v)
}

func (d Dict) GetString(key string) string {
	v, _ := d.getValue(key).(string)
	return v
}

func (d Dict) GetInt(key string) int64 {
	i, _ := d.getValue(key).(int64)
	return i
}

func (d Dict) GetUint(key string) uint64 {
	i := d.GetInt(key)
	return uint64(i)
}

func (d Dict) GetArray(key string) []interface{} {
	v, ok := d.getValue(key).([]interface{})
	if !ok {
		return nil
	}
	return v
}

func (d Dict) GetIntArray(key string) []int64 {
	var (
		arr = d.GetArray(key)
		val []int64
	)
	for i := range arr {
		if n, ok := arr[i].(int64); ok {
			val = append(val, n)
		}
	}
	return val
}

func (d Dict) GetStringArray(key string) []string {
	var (
		arr = d.GetArray(key)
		str []string
	)
	for _, v := range arr {
		s, ok := v.(string)
		if ok {
			str = append(str, s)
		}
	}
	return str
}

func (d Dict) getValue(key string) Value {
	return d[strings.ToLower(key)]
}

func parseValueAsDict(r *Reader, key []byte) (Dict, error) {
	val, err := parseValue(r, key)
	if err != nil {
		return nil, err
	}
	dict, ok := val.(Dict)
	if !ok {
		return nil, fmt.Errorf("not a dict")
	}
	return dict, nil
}

func parseValue(r *Reader, key []byte) (Value, error) {
	skipBlank(r)
	switch b, _ := r.ReadByte(); {
	case b == langle:
		b, _ = r.ReadByte()
		if b == langle {
			return parseDict(r, key)
		}
		r.UnreadByte()
		return parseHex(r, key)
	case b == lparen:
		return parseString(r, key)
	case b == lsquare:
		return parseArray(r, key)
	case b == slash:
		r.UnreadByte()
		return parseName(r)
	case isLetter(b):
		r.UnreadByte()
		return parseIdent(r)
	case isNumber(b):
		r.UnreadByte()
		return parseNumber(r)
	default:
		return nil, fmt.Errorf("parseValue: syntax error (unexpected character %c)", b)
	}
}

func parseArray(r *Reader, key []byte) (Value, error) {
	var (
		arr []interface{}
		err error
		b   byte
	)
	for {
		skipBlank(r)
		b, err = r.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("parseArray: unterminated array")
		}
		if b == rsquare {
			break
		}
		r.UnreadByte()
		v, err := parseValue(r, key)
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
	}
	if b != rsquare {
		return nil, fmt.Errorf("parseArray: unterminated array")
	}
	return arr, nil
}

func parseDict(r *Reader, key []byte) (Value, error) {
	dict := make(Dict)
	for {
		skipBlank(r)
		b, err := r.ReadByte()
		if err != nil {
			break
		}
		if b == rangle {
			b, _ = r.ReadByte()
			if b != rangle {
				return dict, fmt.Errorf("parseDict: unterminated dict (closing character)")
			}
			return dict, nil
		}
		r.UnreadByte()
		name, err := parseName(r)
		if err != nil {
			return dict, err
		}
		value, err := parseValue(r, key)
		if err != nil {
			return dict, fmt.Errorf("parseDict %s: invalid value %w", name, err)
		}
		dict[strings.ToLower(name)] = value
	}
	return dict, fmt.Errorf("parseDict: unterminated dict")
}

func parseNumber(r *Reader) (Value, error) {
	str, err := parseDecimal(r)
	if err != nil {
		return str, err
	}
	if b, _ := r.ReadByte(); b == space {
		var (
			rev string
			ok  bool
		)
		rev, ok, err = parseReference(r)
		if ok && err == nil {
			return fmt.Sprintf("%s/%s", str, rev), nil
		}
	} else {
		r.UnreadByte()
	}
	if n, err := strconv.ParseInt(str, 10, 64); err == nil {
		return n, err
	}
	return strconv.ParseFloat(str, 64)
}

func parseReference(r *Reader) (string, bool, error) {
	tell := r.Tell()
	if tell > 0 {
		tell--
	}
	str, err := parseDecimal(r)
	if err != nil {
		return "", false, err
	}
	if b, _ := r.ReadByte(); b != space {
		r.Seek(tell, io.SeekStart)
		return "", false, nil
	}
	if b, _ := r.ReadByte(); b != 'R' {
		r.Seek(tell, io.SeekStart)
		return "", false, nil
	}
	return str, true, nil
}

func parseDecimal(r *Reader) (string, error) {
	var str bytes.Buffer
	b, _ := r.ReadByte()
	str.WriteByte(b)
	for {
		b, _ := r.ReadByte()
		if !isDigit(b) && b != dot {
			r.UnreadByte()
			break
		}
		str.WriteByte(b)
	}
	return str.String(), nil
}

func parseIdent(r *Reader) (Value, error) {
	var str bytes.Buffer
	for {
		b, _ := r.ReadByte()
		if !isLetter(b) {
			r.UnreadByte()
			break
		}
		str.WriteByte(b)
	}
	ident := str.String()
	switch strings.ToLower(ident) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "obj", "null":
	default:
		return "", fmt.Errorf("parseIdent: %s not a keyword", ident)
	}
	return ident, nil
}

func parseHex(r *Reader, key []byte) (Value, error) {
	var (
		str bytes.Buffer
		err error
		b   byte
	)
	for {
		b, err = r.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("parseHex: unterminated hex string")
		}
		if b == rangle {
			break
		}
		if isSpace(b) {
			skipBlank(r)
			continue
		}
		if !isHex(b) {
			return "", fmt.Errorf("parseHex: invalid character %c", b)
		}
		c1, _ := fromHexChar(b)

		b, _ = r.ReadByte()
		if b == rangle {
			r.UnreadByte()
			b = '0'
		}
		c2, _ := fromHexChar(b)
		str.WriteByte((c1 << 4) | c2)
	}
	if b != rangle {
		return "", fmt.Errorf("parseHex: unterminated string")
	}
	s := convertString(decryptString(key, str.String()))
	return s, nil
}

func parseString(r *Reader, key []byte) (Value, error) {
	var (
		parens int = 1
		str    bytes.Buffer
		err    error
		b      byte
	)
	for {
		b, err = r.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("parseString: unterminated string")
		}
		if b == lparen {
			parens++
		} else if b == rparen {
			parens--
		}
		if b == rparen && parens == 0 {
			break
		}
		if b == backslash {
			b, _ = r.ReadByte()
			if b == nl {
				continue
			}
			switch b {
			case 'n':
				b = nl
			case 'r':
				b = cr
			case 't':
				b = tab
			case 'b':
				b = backspace
			case 'f':
				b = formfeed
			case lparen, rparen, backslash:
			}
		}
		str.WriteByte(b)
	}
	if b != rparen {
		return nil, fmt.Errorf("parseString: unterminated string")
	}
	s := convertString(decryptString(key, str.String()))
	return s, nil
}

func parseName(r *Reader) (string, error) {
	b, _ := r.ReadByte()
	if b != slash {
		return "", fmt.Errorf("parseName: invalid name (missing /)")
	}
	var str bytes.Buffer
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", fmt.Errorf("parseName: unterminated name")
		}
		switch b {
		case pound:
			c1, _ := r.ReadByte()
			c2, _ := r.ReadByte()
			if !isHex(c1) && !isHex(c2) {
				return "", fmt.Errorf("parseName: invalid character")
			}
			c1, _ = fromHexChar(c1)
			c2, _ = fromHexChar(c2)
			b = (c1 << 4) | c2
		case cr, nl, space, tab, langle, lsquare, lparen, rangle, rsquare, rparen, slash:
			r.UnreadByte()
			return str.String(), nil
		}
		str.WriteByte(b)
	}
	return "", nil
}
