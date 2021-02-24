package pdf

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"fmt"
	"image"
	"io"
	"sort"
	"strings"
	"time"
)

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

func Open(file string) (*Document, error) {
	return readFile(file)
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
	return convertString(obj.GetString("lang"))
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
			key := d.getEncryptionKeyForObject(o)
			sig := Signature{
				Who:    decryptString(key, o.GetString("name")),
				Reason: decryptString(key, o.GetString("reason")),
			}
			sig.When, _ = parseTime(decryptString(key, o.GetString("m")))
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
	)
	if obj.isZero() {
		return fi
	}

	fi.Title = obj.GetString("title")
	fi.Author = obj.GetString("author")
	fi.Subject = obj.GetString("subject")
	fi.Creator = obj.GetString("creator")
	fi.Producer = obj.GetString("producer")

	when = obj.GetString("creationdate")
	if strings.HasPrefix(when, "D:") {
		fi.Created, _ = parseTime(when)
	}
	when = obj.GetString("moddate")
	if strings.HasPrefix(when, "D:") {
		fi.Modified, _ = parseTime(when)
	}

	fi.Fields = make(map[string]Value)
	for k := range obj.Dict {
		switch k {
		case "title", "author", "subject", "creator", "producer", "creationdate", "moddate":
		default:
			fi.Fields[k] = obj.Dict[k]
		}
	}
	return fi
}

func (d *Document) GetOutlines() []Outline {
	list := d.getOutlines(d.getOutlinesFromCatalog())
	if len(list) <= 1 {
		return nil
	}
	return list
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
		return nil, fmt.Errorf("empty document")
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
		line := Outline{Title: obj.GetString("title")}
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
	var (
		obj Object
		key []byte
	)
	if oid != d.encrypt {
		key = d.decrypt
	}
	if !d.xref[i].isEmbed() {
		d.inner.Seek(d.xref[i].Offset, io.SeekStart)
		obj, _ = readObject(d.inner, key, full)
	} else {
		obj = d.getObjectWithOid(d.xref[i].Owner, true)
		obj = obj.getEmbeddedObject(d.xref[i].Oid, d.xref[i].Offset)
	}
	return obj
}

func (d *Document) getEncryptionKeyForObject(obj Object) []byte {
	oid, rev := obj.ObjectId()
	return getEncryptionKey(d.decrypt, oid, rev)
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
