package pdf

import (
	"bytes"
	"fmt"
	"io"
)

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

func (r *Reader) ReadValue(key []byte) (Value, error) {
	return parseValue(r, key)
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
