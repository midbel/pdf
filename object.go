package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"strconv"
	"strings"
)

type Pointer struct {
	Oid    string
	Owner  string
	Offset int64
}

func (p Pointer) isEmbed() bool {
	return p.Owner != ""
}

type Object struct {
	Oid string
	Dict
	Data    Value
	Content []byte
}

func (o Object) ObjectId() (int, int) {
	parts := strings.Split(o.Oid, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	var (
		id, _  = strconv.Atoi(parts[0])
		rev, _ = strconv.Atoi(parts[1])
	)
	return id, rev
}

func (o Object) IsSignature() bool {
	return o.isType("Sig")
}

func (o Object) IsPage() bool {
	return o.isType("Page")
}

func (o Object) IsImage() bool {
	return o.isType("xobject") && o.GetString("subtype") == "Image"
}

func (o Object) IsMeta() bool {
	return o.isType("Metadata")
}

func (o Object) IsObjectStream() bool {
	return o.isType("ObjStm")
}

func (o Object) IsXRef() bool {
	return o.isType("XRef")
}

func (o Object) GetResources() Dict {
	if o.IsPage() {
		return o.GetDict("resources")
	}
	return make(Dict)
}

func (o Object) Body() ([]byte, error) {
	var rs io.Reader
	rs = bytes.NewReader(o.Content)
	if o.IsFlate() {
		z, err := zlib.NewReader(rs)
		if err != nil {
			return nil, err
		}
		defer z.Close()
		rs = z
	} else if o.IsLZW() {
		// z := lzw.NewReader(rs)
		// defer z.Close()
		// rs = z
	}
	buf, err := io.ReadAll(rs)
	if err != nil {
		return nil, err
	}
	if !o.Has("decodeparms") {
		return buf, err
	}
	var (
		dict      = o.GetDict("decodeparms")
		predictor = int(dict.GetInt("predictor"))
		columns   = int(dict.GetInt("columns"))
		filtered  []byte
		row       = make([]byte, columns)
	)
	if predictor <= 1 {
		return buf, nil
	}
	for i := 0; i < len(buf); i += columns + 1 {
		for j := 0; j < columns; j++ {
			row[j] = row[j] + buf[i+j+1]
		}
		filtered = append(filtered, row...)
	}
	return filtered, nil
}

func (o Object) isType(str string) bool {
	if o.Dict == nil {
		return false
	}
	return o.GetString("type") == str
}

func (o Object) isZero() bool {
	return o.Oid == "" && (o.Dict == nil || o.Data == nil)
}

func (o Object) GetEmbeddedObjects() []Object {
	if !o.IsObjectStream() {
		return nil
	}
	body, err := o.Body()
	if err != nil {
		return nil
	}
	var (
		first = o.GetInt("first")
		count = int(o.GetInt("n"))
		pairs = bytes.Split(bytes.TrimSpace(body[:first]), []byte{space})
		list  = make([]Object, 0, count)
		rs    = NewReader(body[first:])
	)
	for i := 0; i < count; i++ {
		value, err := parseValue(rs, nil)
		if err != nil {
			continue
		}
		obj := Object{
			Oid:  fmt.Sprintf("%s/0", string(pairs[i*2])),
			Data: value,
		}
		if dict, ok := value.(Dict); ok {
			obj.Dict = dict
		}
		list = append(list, obj)
	}
	return list
}

func (o Object) getEmbeddedObject(oid string, offset int64) Object {
	var obj Object
	if !o.IsObjectStream() {
		return obj
	}
	body, err := o.Body()
	if err != nil {
		return obj
	}
	var (
		first = o.GetInt("first")
		count = o.GetInt("n")
		pairs = bytes.Split(bytes.TrimSpace(body[:first]), []byte{space})
	)
	if offset < 0 || offset >= count {
		return obj
	}
	offset *= 2
	if offset < 0 || offset >= int64(len(pairs)) {
		return obj
	}
	if oid != fmt.Sprintf("%s/0", pairs[offset]) {
		return obj
	}
	offset, _ = strconv.ParseInt(string(pairs[offset+1]), 10, 64)
	r := NewReader(body[first+offset:])

	value, err := parseValue(r, nil)
	if err != nil {
		return obj
	}
	obj.Oid = oid
	obj.Data = value
	if dict, ok := value.(Dict); ok {
		obj.Dict = dict
	}
	return obj
}

func (o Object) readXRef() ([]Pointer, error) {
	buf, err := o.Body()
	if err != nil {
		return nil, err
	}
	var (
		r  = NewReader(buf)
		ix = o.GetIntArray("index")
		ws = o.GetIntArray("w")
		xs = make([]int64, len(ws))
		ps []Pointer
	)
	if len(ix) == 0 {
		ix = append(ix, 0, o.GetInt("size"))
	}
	for j := 0; j < int(ix[1]) && !r.AtEOF(); j++ {
		oid := ix[0] + int64(j)
		for i := 0; i < len(ws); i++ {
			xs[i] = r.ReadInt(ws[i])
		}
		var p Pointer
		switch xs[0] {
		case 1:
			p.Oid = fmt.Sprintf("%d/%d", oid, xs[2])
			p.Offset = xs[1]
		case 2:
			p.Oid = fmt.Sprintf("%d/0", oid)
			p.Owner = fmt.Sprintf("%d/0", xs[1])
			p.Offset = xs[2]
		default:
			continue
		}
		ps = append(ps, p)
	}
	return ps, nil
}

func (o Object) readImage() image.Image {
	img, _ := jpeg.Decode(bytes.NewReader(o.Content))
	return img
}
