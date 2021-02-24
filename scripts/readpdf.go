package main

import (
	"bytes"
	"compress/zlib"
	// "compress/lzw"
	"crypto/md5"
	"crypto/rc4"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	trailer   = []byte("trailer")
	startxref = []byte("startxref")
	eof       = []byte("%%EOF")
	ref       = []byte("xref")
	magic     = []byte("%PDF-1.")
	begobj    = []byte("obj")
	endobj    = []byte("endobj")
	begstream = []byte("stream")
	endstream = []byte("endstream")
)

var (
	ErrMissing = errors.New("not found")
	ErrTrailer = errors.New("trailer")
)

const (
	nl        = '\n'
	cr        = '\r'
	percent   = '%'
	space     = ' '
	tab       = '\t'
	formfeed  = '\f'
	backspace = '\b'
	langle    = '<'
	rangle    = '>'
	lsquare   = '['
	rsquare   = ']'
	lparen    = '('
	rparen    = ')'
	pound     = '#'
	slash     = '/'
	minus     = '-'
	plus      = '+'
	dot       = '.'
	backslash = '\\'
)

const MinRead = 1024

func main() {
	flag.Parse()

	doc, err := readFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("pdf version:", doc.GetVersion())
	fmt.Println("doc lang:", doc.GetLang())
	fmt.Println("doc pages:", doc.GetCount())
	fmt.Printf("info: %+v\n", doc.GetDocumentInfo())
	fmt.Println("outlines:", doc.GetOutlines())
	// fmt.Println("metadata", string(doc.GetDocumentMetadata()))
	doc.Walk(func(o Object) bool {
		fmt.Println(o.Oid, o.Dict)
		for _, o := range o.GetEmbeddedObjects() {
			fmt.Println("embed", o.Oid, o.Data)
		}
		return true
	})

	img := doc.GetImage("image8")
	if img != nil {
		writeImage("tmp/image8.png", img)
	}
}

func writeImage(file string, img image.Image) {
	w, err := os.Create(file)
	if err != nil {
		return
	}
	defer w.Close()

	if err := png.Encode(w, img); err != nil {
		fmt.Println(err)
	}
}

type Signature struct {
	Who    string
	When   time.Time
	Reason string
	Pem    []byte
}

type FileInfo struct {
	Title    string
	Author   string
	Subject  string
	Keywords []string
	Creator  string
	Producer string
	Created  time.Time
	Modified time.Time
	Trapped  bool

	Fields map[string]Value
}

type Outline struct {
	Title string
	Sub   []Outline
}

type Document struct {
	inner *Reader
	xref  []Pointer

	catalog string
	info    string
	encrypt string

	fileid  []string
	decrypt []byte
}

func (d *Document) Close() error {
	return d.inner.Close()
}

func (d *Document) Walk(fn func(Object) bool) error {
	for _, x := range d.xref {
		if x.isEmbed() {
			continue
		}
		obj := d.getObjectWithOid(x.Oid, true)
		if obj.isZero() {
			continue
		}
		if !fn(obj) {
			break
		}
	}
	return nil
}

func (d *Document) GetLang() string {
	obj := d.getCatalog()
	if obj.isZero() {
		return ""
	}
	return obj.GetString("lang")
}

func (d *Document) GetImage(name string) image.Image {
	obj := d.getObjectWithOid(d.getImageOid(name), true)
	if obj.isZero() {
		return nil
	}
	return obj.readImage()
}

func (d *Document) getImageOid(name string) string {
	keep := func(o Object) bool {
		return o.IsPage() && o.Has("resources")
	}
	var oid string
	d.Walk(func(o Object) bool {
		if keep(o) {
			oid = o.GetResources().GetDict("xobject").GetString(name)
		}
		if oid == "" {
			list := o.GetEmbeddedObjects()
			for i := 0; oid == "" && i < len(list); i++ {
				if keep(list[i]) {
					oid = o.GetResources().GetDict("xobject").GetString(name)
				}
			}
		}
		return oid == ""
	})
	return oid
}

func (d *Document) GetDocumentMetadata() []byte {
	obj := d.getCatalog()
	if obj.isZero() {
		return nil
	}
	obj = d.getObjectWithOid(obj.GetString("metadata"), true)
	if obj.isZero() {
		return nil
	}
	var (
		body, _ = obj.Body()
		key     = d.getEncryptionKeyForObject(obj)
	)
	return decryptBytes(key, body)
}

func (d *Document) GetSignatures() []Signature {
	var list []Signature
	d.Walk(func(o Object) bool {
		if o.IsSignature() {
			sig := Signature{
				Who: o.GetString("name"),
				// When:   o.GetTime("m"),
				Reason: o.GetString("reason"),
			}
			list = append(list, sig)
		}
		return true
	})
	return list
}

func (d *Document) GetVersion() string {
	obj := d.getCatalog()
	if !obj.isZero() && obj.Has("version") {
		return obj.GetString("version")
	}
	str := readVersion(d.inner)
	str = bytes.TrimLeft(str, "%PDF-")
	return string(str)
}

func (d *Document) GetDocumentInfo() FileInfo {
	var (
		fi   FileInfo
		when string
		obj  = d.getObjectWithOid(d.info, false)
		key  []byte
	)
	if obj.isZero() {
		return fi
	}

	key = d.getEncryptionKeyForObject(obj)

	fi.Title = decryptString(key, obj.GetString("title"))
	fi.Author = decryptString(key, obj.GetString("author"))
	fi.Subject = decryptString(key, obj.GetString("subject"))
	fi.Creator = decryptString(key, obj.GetString("creator"))
	fi.Producer = decryptString(key, obj.GetString("producer"))

	when = decryptString(key, obj.GetString("creationdate"))
	if strings.HasPrefix(when, "D:") {
		fi.Created, _ = parseTime(when)
	}
	when = decryptString(key, obj.GetString("moddate"))
	if strings.HasPrefix(when, "D:") {
		fi.Modified, _ = parseTime(when)
	}

	fi.Fields = make(map[string]Value)
	for k := range obj.Dict {
		switch k {
		case "title", "author", "subject", "creator", "producer", "creationdate", "moddate":
		default:
			value := obj.Dict[k]
			if s, ok := value.(string); ok {
				value = decryptString(key, s)
			}
			fi.Fields[k] = value
		}
	}
	return fi
}

func (d *Document) GetOutlines() []Outline {
	return d.getOutlines(d.getOutlinesFromCatalog())
}

func (d *Document) GetCount() int64 {
	obj := d.getPageRoot()
	if obj.isZero() {
		return 0
	}
	return obj.GetInt("count")
}

func (d *Document) GetPage(n int) ([]byte, error) {
	obj := d.getPageRoot()
	if obj.isZero() {
		return nil, fmt.Errorf("document seems to be empty")
	}
	if obj = d.getPageObject(obj, n); obj.isZero() {
		return nil, fmt.Errorf("page %d not found in document", n)
	}
	list := obj.GetStringArray("contents")
	if len(list) == 0 {
		list = append(list, obj.GetString("contents"))
	}
	var body []byte
	for _, oid := range list {
		obj = d.getObjectWithOid(oid, true)
		buf, err := obj.Body()
		if err != nil {
			return nil, err
		}
		body = append(body, buf...)
	}
	return body, nil
}

func (d *Document) getPageObject(obj Object, page int) Object {
	if obj.IsPage() {
		return obj
	}
	var (
		kids  = obj.GetStringArray("kids")
		count = int(obj.GetInt("count"))
	)
	if page < 1 || page > count {
		return Object{}
	}
	for _, k := range kids {
		obj = d.getObjectWithOid(k, false)
		if obj.IsPage() || obj.isZero() {
			return obj
		}
		if count = int(obj.GetInt("count")); page <= count {
			kids = obj.GetStringArray("kids")
			return d.getObjectWithOid(kids[page-1], false)
		}
		page -= count
	}
	return Object{}
}

func (d *Document) getOutlinesFromCatalog() Object {
	obj := d.getCatalog()
	if obj.isZero() {
		return obj
	}
	return d.getObjectWithOid(obj.GetString("outlines"), false)
}

func (d *Document) getOutlines(obj Object) []Outline {
	if obj.isZero() {
		return nil
	}
	var (
		first = obj.GetString("first")
		last  = obj.GetString("last")
		lines []Outline
	)
	for obj.Oid != last {
		obj = d.getObjectWithOid(first, false)
		if obj.isZero() {
			return nil
		}
		first = obj.GetString("next")
		var (
			key   = d.getEncryptionKeyForObject(obj)
			title = decryptString(key, obj.GetString("title"))
			line  = Outline{Title: title}
		)
		if obj.Has("first") {
			line.Sub = d.getOutlines(obj)
		}
		lines = append(lines, line)
	}
	return lines
}

func (d *Document) getPageRoot() Object {
	obj := d.getCatalog()
	if obj.isZero() {
		return obj
	}
	return d.getObjectWithOid(obj.GetString("pages"), false)
}

func (d *Document) getCatalog() Object {
	return d.getObjectWithOid(d.catalog, false)
}

func (d *Document) getObjectWithOid(oid string, full bool) Object {
	if oid == "" {
		return Object{}
	}
	i := sort.Search(len(d.xref), func(i int) bool {
		return d.xref[i].Oid <= oid
	})
	if i >= len(d.xref) || d.xref[i].Oid != oid {
		return Object{}
	}
	var obj Object
	if !d.xref[i].isEmbed() {
		d.inner.Seek(d.xref[i].Offset, io.SeekStart)
		obj, _ = readObject(d.inner, full)
	} else {
		obj = d.getObjectWithOid(d.xref[i].Owner, true)
		obj = obj.getEmbeddedObject(d.xref[i].Oid, d.xref[i].Offset)
	}
	return obj
}

func (d *Document) getEncryptionKeyForObject(obj Object) []byte {
	if len(d.decrypt) == 0 {
		return nil
	}
	key := make([]byte, len(d.decrypt))
	copy(key, d.decrypt)

	oid, rev := obj.ObjectId()
	key = append(key, byte(oid), byte(oid>>8), byte(oid>>16))
	key = append(key, byte(rev), byte(rev>>8))

	var (
		sum  = md5.Sum(key)
		size = len(key)
	)
	if size > MaxKeyLength {
		size = MaxKeyLength
	}
	return sum[:size]
}

const MaxKeyLength = 16

var padding = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

func (d *Document) setupKey() error {
	if d.encrypt == "" {
		return nil
	}
	var (
		sum    = md5.New()
		obj    = d.getObjectWithOid(d.encrypt, false)
		user   = obj.GetBytes("u")
		size   = obj.GetInt("length")
		owner  = obj.GetBytes("o")
		access = obj.GetInt("p")
		perm   = uint32(access)
	)

	sum.Write(padding)
	sum.Write(owner)
	sum.Write([]byte{byte(perm), byte(perm >> 8), byte(perm >> 16), byte(perm >> 24)})
	sum.Write([]byte(d.fileid[0]))

	key := sum.Sum(nil)
	for i := 0; i < 50; i++ {
		sum.Reset()
		sum.Write(key[:size/8])
		key = sum.Sum(nil)
	}
	d.decrypt = key[:size/8]

	sum.Reset()
	sum.Write(padding)
	sum.Write([]byte(d.fileid[0]))
	final := sum.Sum(nil)

	ciph, err := rc4.NewCipher(d.decrypt)
	if err != nil {
		return err
	}
	ciph.XORKeyStream(final, final)

	tmp := make([]byte, len(d.decrypt))
	for i := 1; i < 20; i++ {
		copy(tmp, d.decrypt)
		for j := range tmp {
			tmp[j] ^= byte(i)
		}
		c, _ := rc4.NewCipher(tmp)
		c.XORKeyStream(final, final)
	}

	if !bytes.HasPrefix(user, final) {
		return fmt.Errorf("invalid password")
	}
	return nil
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

func (o Object) ObjectId() (uint32, uint32) {
	parts := strings.Split(o.Oid, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	var (
		id, _  = strconv.ParseUint(parts[0], 10, 64)
		rev, _ = strconv.ParseUint(parts[1], 10, 64)
	)
	return uint32(id), uint32(rev)
}

func (o Object) IsSignature() bool {
	return o.isType("sig")
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
		value, err := parseValue(rs)
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

	value, err := parseValue(r)
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
	b, _ := strconv.ParseBool(d.GetString(key))
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
	i, _ := strconv.ParseInt(d.GetString(key), 0, 64)
	return i
}

func (d Dict) GetUint(key string) uint64 {
	i, _ := strconv.ParseUint(d.GetString(key), 0, 64)
	return i
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
		n, _ := strconv.ParseInt(arr[i].(string), 10, 64)
		val = append(val, n)
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

func readFile(file string) (*Document, error) {
	buf, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read file", err)
	}
	var (
		doc Document
		rs  = NewReader(buf)
	)

	linearized, err := readPreamble(rs.Section(0, MinRead))
	if err != nil {
		return nil, fmt.Errorf("read preamble: %s", err)
	}
	if linearized == 0 {
		err = readClassic(rs, &doc)
	} else {
		rs.Seek(linearized, io.SeekStart)
		err = readLinearized(rs, &doc)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(doc.xref, func(i, j int) bool {
		return doc.xref[i].Oid > doc.xref[j].Oid
	})
	doc.inner = rs
	return &doc, doc.setupKey()
}

func readClassic(rs *Reader, doc *Document) error {
	size := rs.Size()
	if size > MinRead {
		size = MinRead
	}
	offset, err := readTrailer(rs.Section(rs.Size()-size, size), doc)
	if err != nil {
		return fmt.Errorf("read trailer: %s", err)
	}

	if doc.xref, err = readXRef(rs.Section(offset, rs.Size()-offset)); err != nil {
		return fmt.Errorf("read xref: %s", err)
	}
	return nil
}

func readLinearized(rs *Reader, doc *Document) error {
	obj, err := readObject(rs, true)
	if err != nil {
		return fmt.Errorf("read object: %s", err)
	}
	doc.encrypt = obj.GetString("encrypt")
	doc.catalog = obj.GetString("root")
	doc.info = obj.GetString("info")
	doc.fileid = obj.GetStringArray("id")

	doc.xref, err = obj.readXRef()
	if err != nil {
		return err
	}
	if offset := obj.GetInt("prev"); offset > 0 {
		rs.Seek(offset, io.SeekStart)
		if obj, err = readObject(rs, true); err != nil {
			return err
		}
		others, err := obj.readXRef()
		if err != nil {
			return err
		}
		doc.xref = append(doc.xref, others...)
		doc.encrypt = obj.GetString("encrypt")
		doc.catalog = obj.GetString("root")
		doc.info = obj.GetString("info")
		doc.fileid = obj.GetStringArray("id")
	}
	return nil
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

func readVersion(r *Reader) []byte {
	r.Seek(0, io.SeekStart)
	line, _ := r.ReadLine()
	return bytes.TrimSpace(line)
}

func readPreamble(r *Reader) (int64, error) {
	if !r.StartsWith(magic) {
		return 0, fmt.Errorf("invalid pdf header! expected %s", magic)
	}
	r.Discard(len(magic))
	switch b, _ := r.ReadByte(); b {
	case '0', '1', '2', '3', '4', '5', '6', '7':
	default:
		return 0, fmt.Errorf("invalid pdf version 1.%c", b)
	}
	r.Skip()
	if _, err := r.ReadLine(); err != nil {
		return 0, err
	}
	for {
		if b, _ := r.ReadByte(); b == percent {
			r.ReadLine()
		} else {
			r.UnreadByte()
			break
		}
	}
	obj, err := readObject(r, false)
	if err == nil && !obj.isZero() && obj.Linearized() {
		return r.Tell(), nil
	}
	return 0, err
}

func readObject(r *Reader, full bool) (Object, error) {
	r.Skip()
	var (
		oid int
		rev int
		typ string
		obj Object
	)
	if _, err := fmt.Fscanf(r, "%d %d %s", &oid, &rev, &typ); err != nil {
		return obj, fmt.Errorf("fail to scan object header: %w", err)
	}
	if !bytes.Equal([]byte(typ), begobj) {
		return obj, fmt.Errorf("object keyword %w", ErrMissing)
	}
	obj.Oid = fmt.Sprintf("%d/%d", oid, rev)
	val, err := parseValue(r)
	if err != nil {
		return obj, err
	}
	if d, ok := val.(Dict); ok {
		obj.Dict = d
	} else {
		obj.Data = val
	}

	switch line, _ := r.ReadLine(); {
	case bytes.Equal(line, endobj):
	case bytes.Equal(line, begstream):
		if !full {
			break
		}
		obj.Content = make([]byte, obj.GetInt("length"))
		if _, err := io.ReadFull(r, obj.Content); err != nil {
			return obj, err
		}
		if line, _ = r.ReadLine(); !bytes.Equal(line, endstream) {
			return obj, fmt.Errorf("%s %w", endstream, ErrMissing)
		}
		if line, _ = r.ReadLine(); !bytes.Equal(line, endobj) {
			return obj, fmt.Errorf("%s %w", endobj, ErrMissing)
		}
	default:
		return obj, fmt.Errorf("unexpected keyword %s", line)
	}
	return obj, nil
}

func readXRef(r *Reader) ([]Pointer, error) {
	if !r.StartsWith(ref) {
		return nil, fmt.Errorf("xref %w", ErrMissing)
	}
	r.Discard(len(ref))
	r.Skip()
	var (
		first int
		num   int
	)
	_, err := fmt.Fscanf(r, "%d %d", &first, &num)
	if err != nil {
		return nil, err
	}
	r.Skip()
	ps := make([]Pointer, 0, num)
	for i := 0; i < num; i++ {
		var (
			off int
			rev int
			typ string
		)
		if _, err = fmt.Fscanf(r, "%d %d %s\r\n", &off, &rev, &typ); err != nil {
			return nil, err
		}
		if typ == "f" {
			continue
		}
		p := Pointer{
			Oid:    fmt.Sprintf("%d/%d", first+i, rev),
			Offset: int64(off),
		}
		ps = append(ps, p)
	}
	return ps, nil
}

func readTrailer(r *Reader, doc *Document) (int64, error) {
	x := r.Index(trailer)
	if x < 0 {
		x = r.Index(startxref)
		if x < 0 {
			return 0, fmt.Errorf("%w %s", ErrTrailer, ErrMissing)
		}
		r.Discard(len(startxref) + x)
		r.Skip()

		xref, _ := r.ReadLine()
		if !r.StartsWith(eof) {
			return 0, fmt.Errorf("%s %w", eof, ErrMissing)
		}
		return strconv.ParseInt(string(xref), 10, 64)
	}
	r.Discard(len(trailer) + x)
	r.Skip()

	dict, err := parseValueAsDict(r)
	if err != nil {
		return 0, err
	}
	doc.encrypt = dict.GetString("encrypt")
	doc.catalog = dict.GetString("root")
	doc.info = dict.GetString("info")
	doc.fileid = dict.GetStringArray("id")

	r.Skip()

	if !r.StartsWith(startxref) {
		return 0, fmt.Errorf("%s %w", startxref, ErrMissing)
	}
	r.Discard(len(startxref))

	xref, _ := r.ReadLine()
	if !r.StartsWith(eof) {
		return 0, fmt.Errorf("%s %w", eof, ErrMissing)
	}
	return strconv.ParseInt(string(xref), 10, 64)
}

type Reader struct {
	buf []byte
	ptr int
}

func NewReader(b []byte) *Reader {
	return &Reader{
		buf: b,
		ptr: 0,
	}
}

func (r *Reader) Close() error {
	if len(r.buf) == 0 {
		return fmt.Errorf("reader already closed")
	}
	r.buf = r.buf[:0]
	return nil
}

func (r *Reader) AtEOF() bool {
	return r.ptr >= len(r.buf)
}

func (r *Reader) Section(offset, size int64) *Reader {
	return NewReader(r.buf[offset : offset+size])
}

func (r *Reader) Size() int64 {
	return int64(len(r.buf))
}

func (r *Reader) Len() int {
	if r.ptr >= len(r.buf) {
		return 0
	}
	return len(r.buf) - r.ptr
}

func (r *Reader) Index(b []byte) int {
	if r.ptr > len(r.buf) {
		return -1
	}
	return bytes.Index(r.buf[r.ptr:], b)
}

func (r *Reader) IndexByte(b byte) int {
	if r.ptr > len(r.buf) {
		return -1
	}
	return bytes.IndexByte(r.buf[r.ptr:], b)
}

func (r *Reader) Bytes() []byte {
	if r.ptr >= len(r.buf) {
		return nil
	}
	return r.buf[r.ptr:]
}

func (r *Reader) ReadLine() ([]byte, error) {
	var (
		line []byte
		err  error
	)
	for {
		line, err = r.readLine()
		if err != nil || len(line) > 0 {
			break
		}
	}
	return line, err
}

func (r *Reader) Skip() {
	skipBlank(r)
}

func (r *Reader) StartsWith(b []byte) bool {
	if r.ptr >= len(r.buf) {
		return false
	}
	return bytes.HasPrefix(r.buf[r.ptr:], b)
}

func (r *Reader) EndsWith(b []byte) bool {
	if r.ptr >= len(r.buf) {
		return false
	}
	return bytes.HasSuffix(r.buf[r.ptr:], b)
}

func (r *Reader) Peek(n int) ([]byte, error) {
	if r.ptr > len(r.buf) {
		return nil, io.EOF
	}
	if end := r.ptr + n; end > len(r.buf) {
		n = len(r.buf) - r.ptr
	}
	buf := make([]byte, n)
	copy(buf, r.buf[r.ptr:])
	return buf, nil
}

func (r *Reader) Discard(n int) (int, error) {
	if r.ptr >= len(r.buf) {
		return 0, io.EOF
	}
	r.ptr += n
	if r.ptr >= len(r.buf) {
		n = r.ptr - len(r.buf)
		r.ptr = len(r.buf)
	}
	return n, nil
}

func (r *Reader) Read(b []byte) (int, error) {
	if r.ptr >= len(r.buf) {
		return 0, io.EOF
	}
	n := copy(b, r.buf[r.ptr:])
	r.ptr += n
	return n, nil
}

func (r *Reader) ReadAt(b []byte, offset int64) (n int, err error) {
	if offset < 0 {
		return 0, fmt.Errorf("readat: negative offset")
	}
	if offset >= r.Size() {
		return 0, io.EOF
	}
	n = copy(b, r.buf[offset:])
	if n < len(r.buf) {
		err = io.EOF
	}
	return n, err
}

func (r *Reader) Tell() int64 {
	return int64(r.ptr)
}

func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		r.ptr = int(offset)
	case io.SeekCurrent:
		r.ptr += int(offset)
	case io.SeekEnd:
		r.ptr = len(r.buf) + int(offset)
	default:
		return 0, fmt.Errorf("seek: invalid whence")
	}
	if r.ptr < 0 {
		return 0, fmt.Errorf("seek: negative position")
	}
	return int64(r.ptr), nil
}

func (r *Reader) ReadInt(n int64) int64 {
	if r.ptr > len(r.buf) {
		return 0
	}
	var z int64
	for i := n - 1; i >= 0; i-- {
		b, _ := r.ReadByte()
		z |= int64(b) << (i * 8)
	}
	return z
}

func (r *Reader) ReadByte() (byte, error) {
	if r.ptr >= len(r.buf) {
		return 0, io.EOF
	}
	b := r.buf[r.ptr]
	r.ptr++
	return b, nil
}

func (r *Reader) UnreadByte() error {
	if r.ptr <= 0 {
		return nil
	}
	r.ptr--
	return nil
}

func (r *Reader) readLine() ([]byte, error) {
	if r.ptr >= len(r.buf) {
		return nil, io.EOF
	}
	offset := indexNL(r.buf[r.ptr:]) + 1
	if offset <= 0 {
		offset = len(r.buf) - r.ptr
	}
	buf := make([]byte, offset)
	r.ptr += copy(buf, r.buf[r.ptr:])
	return bytes.TrimSpace(buf), nil
}

func indexNL(buf []byte) int {
	var (
		crix   = bytes.IndexByte(buf, cr)
		nlix   = bytes.IndexByte(buf, nl)
		offset int
	)

	if crix >= 0 {
		if crix < len(buf) && buf[crix+1] == nl {
			crix++
		}
		offset = crix
		if nlix >= 0 && nlix < crix-1 {
			offset = nlix
		}
	} else if nlix >= 0 {
		offset = nlix
	}
	return offset
}

func parseValueAsDict(r *Reader) (Dict, error) {
	val, err := parseValue(r)
	if err != nil {
		return nil, err
	}
	dict, ok := val.(Dict)
	if !ok {
		return nil, fmt.Errorf("not a dict")
	}
	return dict, nil
}

func parseValue(r *Reader) (Value, error) {
	skipBlank(r)
	switch b, _ := r.ReadByte(); {
	case b == langle:
		b, _ = r.ReadByte()
		if b == langle {
			return parseDict(r)
		}
		r.UnreadByte()
		return parseHex(r)
	case b == lparen:
		return parseString(r)
	case b == lsquare:
		return parseArray(r)
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

func parseArray(r *Reader) (Value, error) {
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
		v, err := parseValue(r)
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

func parseDict(r *Reader) (Value, error) {
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
		value, err := parseValue(r)
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
	return str, err
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
	case "true", "false", "obj", "null":
	default:
		return "", fmt.Errorf("parseIdent: %s not a keyword", ident)
	}
	return ident, nil
}

func parseHex(r *Reader) (Value, error) {
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
	return str.String(), nil
}

func parseString(r *Reader) (Value, error) {
	var (
		parens int = 1
		str    bytes.Buffer
		err    error
		b      byte
	)
	if b, _ = r.ReadByte(); b == 0xfe {
		r.ReadByte()
	} else {
		r.UnreadByte()
	}
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
	return str.String(), nil
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

func skipBlank(r *Reader) {
	for {
		b, _ := r.ReadByte()
		if !isBlank(b) {
			r.UnreadByte()
			break
		}
	}
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
