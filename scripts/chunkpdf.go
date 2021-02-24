package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"crypto/rc4"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/midbel/hexdump"
)

const (
	begObject    = "obj"
	endObject    = "endobj"
	begStream    = "stream"
	endStream    = "endstream"
	objPattern   = "%d %d %s"
	pdfStartxref = "startxref"
	pdfXref      = "xref"
	pdfTrailer   = "trailer"
	pdfEOF       = "%%EOF"
	pdfMagic     = "%PDF-1."
)

type Value interface{}

type Dict map[string]Value

func (d Dict) Linearized() bool {
	v := d.GetString("linearized")
	return v == "1"
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

func (d Dict) Length() int64 {
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

func (d Dict) GetTime(key string) time.Time {
	t, _ := d.getValue(key).(time.Time)
	return t
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

type Object struct {
	Oid     string
	dict    Dict
	data    Value
	content []byte
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

func (o Object) IsPage() bool {
	return o.isType("Page")
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

func (o Object) isType(str string) bool {
	if o.dict == nil {
		return false
	}
	return o.dict.GetString("type") == str
}

func (o Object) unwrapReferences() error {
	getWidth := func() []int64 {
		var (
			vs = o.dict["w"].([]interface{})
			xs = make([]int64, len(vs))
		)
		for i := range vs {
			xs[i], _ = strconv.ParseInt(vs[i].(string), 0, 64)
		}
		return xs
	}
	pickVal := func(r io.ByteReader, n int64) int64 {
		var z int64
		for i := n - 1; i >= 0; i-- {
			b, _ := r.ReadByte()
			z |= int64(b) << (i * 8)
		}
		return z
	}
	buf, err := o.Body()
	if err != nil {
		return err
	}
	var (
		br = bufio.NewReader(bytes.NewReader(buf))
		ws = getWidth()
		xs = make([]int64, len(ws))
	)
	for j := 0; ; j++ {
		for i := 0; i < len(ws); i++ {
			xs[i] = pickVal(br, ws[i])
		}
		if _, err := br.ReadByte(); err != nil {
			break
		}
		br.UnreadByte()
	}
	return nil
}

func (o Object) Body() ([]byte, error) {
	var rs io.Reader
	rs = bytes.NewReader(o.content)
	if o.dict.IsFlate() {
		z, err := zlib.NewReader(rs)
		if err != nil {
			return nil, err
		}
		defer z.Close()
		rs = z
	}
	buf, err := io.ReadAll(rs)
	if err != nil {
		return nil, err
	}
	if !o.dict.Has("decodeparms") {
		return buf, err
	}
	var (
		params    = o.dict.GetDict("decodeparms")
		predictor = int(params.GetInt("predictor"))
		columns   = int(params.GetInt("columns"))
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

func (o Object) unwrapObjects() ([]Object, error) {
	buf, err := o.Body()
	if err != nil {
		return nil, err
	}
	var (
		first = o.dict.GetInt("first")
		objs  = make([]Object, o.dict.GetInt("n"))
		str   = strings.Split(string(bytes.TrimSpace(buf[:first])), " ")
	)
	for i, j := 0, 0; i < len(objs); i++ {
		var (
			oid, _    = strconv.ParseInt(str[j], 0, 64)
			offset, _ = strconv.ParseInt(str[j+1], 0, 64)
			length    int64
		)
		offset += first
		if x := j + 3; x >= len(str) {
			length = int64(len(buf)) - offset
		} else {
			next, _ := strconv.ParseInt(str[x], 0, 64)
			length = (next + first) - offset
		}
		val, err := parseValueFrom(buf[offset : offset+length])
		if err != nil {
			return nil, err
		}
		objs[i].Oid = fmt.Sprintf("%d/0", oid)
		objs[i].data = val
		if dict, ok := val.(Dict); ok {
			objs[i].dict = dict
		}
		fmt.Printf("obj: %s (wrapped)\n", objs[i].Oid)
		fmt.Println(val)

		j += 2
	}
	return objs, nil
}

func (o Object) isZero() bool {
	return o.Oid == "" && o.dict == nil && o.data == nil
}

type Info struct {
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

type Signature struct {
	Who    string
	When   time.Time
	Reason string
	Pem    []byte
}

type Document struct {
	version string
	meta    []Object
	objects []Object
	pages   []Object

	catalog string
	info    string
	encrypt string
	fileid  []string

	decrypt []byte
}

func (d *Document) GetPagesCount() int64 {
	obj := d.getPageRoot()
	if obj.isZero() {
		return 0
	}
	return obj.dict.GetInt("count")
}

func (d *Document) GetPage(n int) ([]byte, error) {
	obj := d.getPageRoot()
	if obj.isZero() {
		return nil, fmt.Errorf("document seems to be empty")
	}
	if obj = d.getPageObject(obj, n); obj.isZero() {
		return nil, fmt.Errorf("page %d not found in document", n)
	}
	list := obj.dict.GetStringArray("contents")
	if len(list) == 0 {
		list = append(list, obj.dict.GetString("contents"))
	}
	var body []byte
	for _, oid := range list {
		obj = d.getObjectWithOid(oid)
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
		kids  = obj.dict.GetStringArray("kids")
		count = int(obj.dict.GetInt("count"))
	)
	if page < 1 || page > count {
		return Object{}
	}
	for _, k := range kids {
		obj = d.getObjectWithOid(k)
		if obj.IsPage() || obj.isZero() {
			return obj
		}
		if count = int(obj.dict.GetInt("count")); page <= count {
			kids = obj.dict.GetStringArray("kids")
			return d.getObjectWithOid(kids[page-1])
		}
		page -= count
	}
	return Object{}
}

func (d *Document) getPageRoot() Object {
	obj := d.getObjectWithOid(d.catalog)
	if obj.isZero() {
		return obj
	}
	return d.getObjectWithOid(obj.dict.GetString("pages"))
}

func (d *Document) IsEncrypted() (bool, bool) {
	obj := d.getObjectWithOid(d.encrypt)
	return !obj.isZero(), false
}

var padding = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

func (d *Document) setupKey() error {
	var (
		sum    = md5.New()
		obj    = d.getObjectWithOid(d.encrypt)
		user   = obj.dict.GetBytes("u")
		size   = obj.dict.GetInt("length")
		owner  = obj.dict.GetBytes("o")
		access = obj.dict.GetInt("p")
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

func (d *Document) GetMetadata() error {
	return nil
}

func (d *Document) GetSignatures() []Signature {
	var (
		list = d.findObjectsWithType("Sig")
		set  = make([]Signature, 0, len(list))
	)
	for _, o := range list {
		sig := Signature{
			Who:    o.dict.GetString("name"),
			When:   o.dict.GetTime("m"),
			Reason: o.dict.GetString("reason"),
		}
		set = append(set, sig)
	}
	return set
}

func (d *Document) GetOutlines() []Outline {
	return d.getOutlines(d.getOutlinesFromCatalog())
}

func (d *Document) getOutlines(obj Object) []Outline {
	if obj.isZero() {
		return nil
	}
	var (
		first = obj.dict.GetString("first")
		last  = obj.dict.GetString("last")
		lines []Outline
	)
	for obj.Oid != last {
		obj = d.getObjectWithOid(first)
		if obj.isZero() {
			return nil
		}
		first = obj.dict.GetString("next")
		var (
			key   = d.getEncryptionKeyForObject(obj)
			title = decryptString(key, obj.dict.GetString("title"))
			line  = Outline{Title: title}
		)
		if obj.dict.Has("first") {
			line.Sub = d.getOutlines(obj)
		}
		lines = append(lines, line)
	}
	return lines
}

func (d *Document) getOutlinesFromCatalog() Object {
	catalog := d.getObjectWithOid(d.catalog)
	if catalog.isZero() {
		return Object{}
	}
	return d.getObjectWithOid(catalog.dict.GetString("outlines"))
}

func (d *Document) GetDocumentInfo() Info {
	var (
		i Info
		c = d.getObjectWithOid(d.info)
	)
	if c.isZero() {
		return i
	}

	key := d.getEncryptionKeyForObject(c)

	i.Title = decryptString(key, c.dict.GetString("title"))
	i.Author = decryptString(key, c.dict.GetString("author"))
	i.Subject = decryptString(key, c.dict.GetString("subject"))
	i.Creator = decryptString(key, c.dict.GetString("creator"))
	i.Producer = decryptString(key, c.dict.GetString("producer"))
	i.Created = c.dict.GetTime("creationdate")
	i.Modified = c.dict.GetTime("moddate")

	i.Fields = make(map[string]Value)
	for k := range c.dict {
		switch k {
		case "title", "author", "subject", "creator", "producer", "creationdate", "moddate":
		default:
			i.Fields[k] = c.dict[k]
		}
	}

	return i
}

func (d *Document) findObjectsWithType(typ string) []Object {
	var list []Object
	for _, o := range d.objects {
		if o.dict.Type() == typ {
			list = append(list, o)
		}
	}
	return list
}

func (d *Document) getObjectWithOid(oid string) Object {
	if oid == "" {
		return Object{}
	}
	i := sort.Search(len(d.objects), func(i int) bool {
		return d.objects[i].Oid <= oid
	})
	if i >= len(d.objects) || d.objects[i].Oid != oid {
		return Object{}
	}
	return d.objects[i]
}

func main() {
	page := flag.Int("p", 1, "page number")
	flag.Parse()

	for i, a := range flag.Args() {
		if i > 0 {
			fmt.Println(strings.Repeat("-", 80))
		}
		doc, err := readFile(a)
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(os.Stderr, err)
			continue
		}

		fmt.Println(doc.IsEncrypted())

		fmt.Printf("info: %+v\n", doc.GetDocumentInfo())
		fmt.Println("==")
		fmt.Println("outline", doc.GetOutlines())
		fmt.Println("==")
		fmt.Println("signature", doc.GetSignatures())
		fmt.Println("==")
		fmt.Println("metadata", doc.GetMetadata())
		fmt.Println("==")
		fmt.Println("pages", doc.GetPagesCount())
		fmt.Println("========")
		body, err := doc.GetPage(*page)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(len(body))
		size := len(body)
		if size > 256 {
			size = 256
		}
		fmt.Println(hexdump.Dump(body[:size]))
	}
}

func readFile(file string) (*Document, error) {
	buf, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	var (
		doc Document
		rs  = bytes.NewReader(buf)
	)
	if err := readPreamble(&doc, rs); err != nil {
		return nil, err
	}
	for {
		line, err := readLine(rs)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if !keepLine(line) {
			continue
		}
		if str := string(line); str == pdfStartxref {
			err = readStartxref(&doc, rs)
		} else if str == pdfXref {
			err = readReference(&doc, rs)
		} else if str == pdfTrailer {
			err = readTrailer(&doc, rs)
		} else {
			err = readObject(&doc, line, rs)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}
	sort.Slice(doc.objects, func(i, j int) bool {
		return doc.objects[i].Oid > doc.objects[j].Oid
	})
	sort.Slice(doc.meta, func(i, j int) bool {
		return doc.meta[i].Oid > doc.meta[j].Oid
	})
	if ok, _ := doc.IsEncrypted(); ok {
		doc.setupKey()
	}
	return &doc, nil
}

func keepLine(line []byte) bool {
	if len(line) == 0 {
		return false
	}
	return line[0] != percent
}

func readTrailer(doc *Document, r io.ReadSeeker) error {
	buf, err := readValue(r)
	if err != nil {
		return err
	}
	value, err := parseValueFrom(buf)
	if dict, ok := value.(Dict); ok && err == nil {
		doc.catalog = dict.GetString("root")
		doc.info = dict.GetString("info")
		doc.encrypt = dict.GetString("encrypt")
		doc.fileid = dict.GetStringArray("id")
	}
	return nil
}

func readReference(doc *Document, r io.ReadSeeker) error {
	readFrom := func() (io.Reader, error) {
		buf, err := readLine(r)
		if err != nil {
			return nil, err
		}
		return bytes.NewReader(buf), nil
	}
	var (
		first int
		elem  int
	)
	rs, err := readFrom()
	if err != nil {
		return err
	}
	if _, err := fmt.Fscanf(rs, "%d %d", &first, &elem); err != nil {
		return fmt.Errorf("%w: xref header", err)
	}
	var (
		offset int
		rev    int
		char   string
	)
	for i := 0; i < int(elem); i++ {
		rs, err = readFrom()
		if err != nil {
			return err
		}
		if _, err := fmt.Fscanf(rs, "%10d %5d %1s", &offset, &rev, &char); err != nil {
			return fmt.Errorf("%w: xref item", err)
		}
	}
	return nil
}

func readPreamble(doc *Document, r io.ReadSeeker) error {
	buf, err := readLine(r)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(buf, []byte(pdfMagic)) {
		return fmt.Errorf("invalid PDF document")
	}
	return nil
}

func extractDict(buffer []byte) int64 {
	var (
		ptr   int
		count = 1
	)
	for i := 2; i < len(buffer) && count > 0; i++ {
		c := buffer[i]
		if c == langle && buffer[i+1] == langle {
			i++
			count++
		}
		if c == rangle && buffer[i+1] == rangle {
			i++
			count--
		}
		ptr = i
	}
	if count > 0 {
		return -1
	}
	return int64(ptr) + 1
}

func extractUntil(buffer []byte, left, right byte) int64 {
	var (
		ptr   int
		count = 1
	)
	for i := 1; i < len(buffer) && count > 0; i++ {
		c := buffer[i]
		if c == left {
			count++
		}
		if c == right {
			count--
		}
		ptr = i
	}
	if count > 0 {
		return -1
	}
	return int64(ptr) + 1
}

func readValue(r io.ReadSeeker) ([]byte, error) {
	var (
		buffer = make([]byte, 4096)
		chunk  []byte
	)
	tell, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	for {
		n, err := r.Read(buffer)
		if err != nil {
			return nil, err
		}
		if n < len(buffer) {
			buffer = buffer[:n]
		}
		if len(chunk) == 0 {
			buffer = bytes.TrimLeft(buffer, "\x20\x0d\x0a")
			tell += int64(n - len(buffer))
			n = len(buffer)
		}
		chunk = append(chunk, buffer[:n]...)
		var offset int64
		switch chunk[0] {
		case langle:
			if chunk[1] == langle {
				offset = extractDict(chunk)
			} else {
				offset = extractUntil(chunk, langle, rangle)
			}
		case lsquare:
			offset = extractUntil(chunk, lsquare, rsquare)
		case lparen:
			offset = extractUntil(chunk, lparen, rparen)
		default:
			offset = IndexNL(chunk)
			if offset <= 0 {
				return nil, fmt.Errorf("invalid object value (%02x)", chunk[0])
			}
		}
		if offset > 0 {
			chunk = chunk[:offset]
			if _, err := r.Seek(tell+offset, io.SeekStart); err != nil {
				return nil, err
			}
			break
		}
	}
	chunk = bytes.ReplaceAll(chunk, []byte{cr}, nil)
	return bytes.TrimSpace(chunk), nil
}

func IndexNL(buf []byte) int64 {
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
	return int64(offset)
}

func readLine(r io.ReadSeeker) ([]byte, error) {
	var (
		buffer = make([]byte, 4096)
		chunk  []byte
	)
	for {
		tell, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		n, err := r.Read(buffer)
		if err != nil {
			return nil, err
		}
		buf := bytes.TrimLeft(buffer, "\x0d\x0a\x20")
		if len(buf) < n {
			tell += int64(n - len(buf))
		}

		if offset := IndexNL(buf); offset > 0 {
			chunk = append(chunk, buf[:offset]...)
			_, err := r.Seek(tell+offset+1, io.SeekStart)
			if err != nil {
				return nil, err
			}
			break
		}
		chunk = append(chunk, buf[:n]...)
	}
	return bytes.TrimSpace(chunk), nil
}

func readObject(doc *Document, preamble []byte, r io.ReadSeeker) error {
	var (
		oid   int
		rev   int
		obj   Object
		label string
	)
	if _, err := fmt.Sscanf(string(preamble), objPattern, &oid, &rev, &label); err != nil {
		return err
	}
	obj.Oid = fmt.Sprintf("%d/%d", oid, rev)
	if label != begObject {
		return fmt.Errorf("invalid object header %s", preamble)
	}
	dict, err := readValue(r)
	if err != nil {
		return err
	}
	val, err := parseValueFrom(dict)
	if err != nil {
		return err
	}
	if dict, ok := val.(Dict); ok {
		fmt.Printf("obj: %s %s\n", obj.Oid, dict.Type())
		fmt.Println(dict)
		obj.dict = dict
	} else {
		obj.data = val
	}

	line, err := readLine(r)
	if err != nil {
		return err
	}
	switch str := string(line); str {
	case endObject:
	case begStream:
		if obj.content, err = readStream(r); err != nil {
			return err
		}
		line, err := readLine(r)
		if err == nil && string(line) != endObject {
			err = fmt.Errorf("invalid object trailer: %s", line)
		}
	default:
		err = fmt.Errorf("kaboum invalid %s", str)
	}
	if err != nil {
		return err
	}
	if obj.IsXRef() {
		doc.catalog = obj.dict.GetString("root")
		doc.info = obj.dict.GetString("info")
		doc.encrypt = obj.dict.GetString("encrypt")
		doc.fileid = obj.dict.GetStringArray("id")
	} else if obj.IsMeta() {
		doc.meta = append(doc.meta, obj)
	} else if obj.IsObjectStream() {
		set, err := obj.unwrapObjects()
		if err != nil {
			return err
		}
		doc.objects = append(doc.objects, set...)
	} else {
		doc.objects = append(doc.objects, obj)
	}
	return nil
}

func readStartxref(doc *Document, r io.ReadSeeker) error {
	for {
		buffer, err := readLine(r)
		if err != nil {
			return err
		}
		if string(buffer) == pdfEOF {
			break
		}
	}
	return nil
}

func readStream(r io.ReadSeeker) ([]byte, error) {
	tell, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	var (
		buffer = make([]byte, 4096)
		chunk  []byte
	)
	for {
		n, err := r.Read(buffer)
		if err != nil {
			return nil, err
		}
		chunk = append(chunk, buffer[:n]...)
		ix := bytes.Index(chunk, []byte(endStream))
		if ix >= 0 {
			chunk = chunk[:ix]
			if _, err := r.Seek(tell+int64(len(chunk)), io.SeekStart); err != nil {
				return nil, err
			}
			buf, err := readLine(r)
			if err != nil {
				return nil, err
			}
			if string(buf) != endStream {
				return nil, fmt.Errorf("invalid stream trailer!!!")
			}
			break
		}
	}
	return chunk, nil
}

const (
	cr        = '\r'
	nl        = '\n'
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
	percent   = '%'
)

func parseValueFrom(str []byte) (Value, error) {
	return parseValue(strings.NewReader(string(str)))
}

func parseValue(r *strings.Reader) (Value, error) {
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

func parseArray(r *strings.Reader) (Value, error) {
	var (
		arr []interface{}
		b   byte
	)
	for r.Len() > 0 {
		skipBlank(r)
		b, _ = r.ReadByte()
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
	if r.Len() == 0 && b != rsquare {
		return nil, fmt.Errorf("parseArray: unterminated array")
	}
	return arr, nil
}

func parseDict(r *strings.Reader) (Value, error) {
	dict := make(Dict)
	for r.Len() > 0 {
		skipBlank(r)
		b, _ := r.ReadByte()
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
	skipBlank(r)
	return dict, fmt.Errorf("parseDict: unterminated dict")
}

func parseNumber(r *strings.Reader) (Value, error) {
	str, err := parseDecimal(r)
	if err != nil {
		return str, err
	}
	b, _ := r.ReadByte()
	if b == space {
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

func parseReference(r *strings.Reader) (string, bool, error) {
	tell, _ := r.Seek(0, io.SeekCurrent)
	if tell > 0 {
		tell--
	}
	str, err := parseDecimal(r)
	if err != nil {
		return "", false, err
	}
	b, _ := r.ReadByte()
	if b != space {
		r.Seek(tell, io.SeekStart)
		return "", false, nil
	}
	if b, _ = r.ReadByte(); b != 'R' {
		r.Seek(tell, io.SeekStart)
		return "", false, nil
	}
	return str, true, nil
}

func parseDecimal(r *strings.Reader) (string, error) {
	var str strings.Builder
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
	return str.String(), nil
}

func parseIdent(r *strings.Reader) (Value, error) {
	var str strings.Builder
	for r.Len() > 0 {
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

func parseHex(r *strings.Reader) (Value, error) {
	var (
		str strings.Builder
		b   byte
	)
	for r.Len() > 0 {
		b, _ = r.ReadByte()
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
	if r.Len() == 0 && b != rangle {
		return "", fmt.Errorf("parseHex: unterminated string")
	}
	return str.String(), nil
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

func parseString(r *strings.Reader) (Value, error) {
	var (
		parens int = 1
		str    strings.Builder
		b      byte
	)
	if b, _ = r.ReadByte(); b == 0xfe {
		r.ReadByte()
	} else {
		r.UnreadByte()
	}
	for r.Len() > 0 {
		b, _ = r.ReadByte()
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
	if r.Len() == 0 && b != rparen {
		return "", fmt.Errorf("parseString: unterminated string")
	}
	ident := str.String()
	if strings.HasPrefix(ident, "D:") {
		return parseTime(ident)
	}
	return ident, nil
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

func parseName(r *strings.Reader) (string, error) {
	b, _ := r.ReadByte()
	if b != slash {
		return "", fmt.Errorf("parseName: invalid name (missing /)")
	}
	var str strings.Builder
	for r.Len() > 0 {
		b, _ := r.ReadByte()
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
	return str.String(), nil
}

func skipBlank(r *strings.Reader) {
	for r.Len() > 0 {
		b, _ := r.ReadByte()
		if !isBlank(b) {
			r.UnreadByte()
			break
		}
	}
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
