package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
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

const MinRead = 1024

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
	switch {
	case err == nil:
	case errors.Is(err, ErrTrailer):
		rs.Seek(offset, io.SeekStart)
		return readLinearized(rs, doc)
	default:
		return fmt.Errorf("read trailer: %s", err)
	}

	if doc.xref, err = readXRef(rs.Section(offset, rs.Size()-offset)); err != nil {
		return fmt.Errorf("read xref: %s", err)
	}
	return nil
}

func readLinearized(rs *Reader, doc *Document) error {
	obj, err := readObject(rs, nil, true)
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
		if obj, err = readObject(rs, nil, true); err != nil {
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
	obj, err := readObject(r, nil, false)
	if err == nil && !obj.isZero() && obj.Linearized() {
		return r.Tell(), nil
	}
	return 0, err
}

func readObject(r *Reader, key []byte, full bool) (Object, error) {
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
	key = getEncryptionKey(key, oid, rev)
	obj.Oid = fmt.Sprintf("%d/%d", oid, rev)

	val, err := r.ReadValue(key)
	if err != nil {
		return obj, err
	}
	if d, ok := val.(Dict); ok {
		if d.IsEmpty() {
			return obj, nil
		}
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
		tmp := make([]byte, obj.GetInt("length"))
		if _, err := io.ReadFull(r, tmp); err != nil {
			return obj, err
		}
		obj.Content = decryptBytes(key, tmp)
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
			return 0, fmt.Errorf("%s %w", startxref, ErrMissing)
		}
		r.Discard(len(startxref) + x)
		offset, err := readStartxref(r)
		if err == nil {
			err = ErrTrailer
		}
		return offset, err
	}
	r.Discard(len(trailer) + x)
	r.Skip()

	dict, err := parseValueAsDict(r, nil)
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
	return readStartxref(r)
}

func readStartxref(r *Reader) (int64, error) {
	xref, _ := r.ReadLine()
	if !r.StartsWith(eof) {
		return 0, fmt.Errorf("%s %w", eof, ErrMissing)
	}
	return strconv.ParseInt(string(xref), 10, 64)
}
