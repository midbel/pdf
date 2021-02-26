package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/midbel/pdf"
)

func main() {
	var rg Range
	flag.Var(&rg, "p", "page range")
	flag.Parse()
	doc, err := pdf.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer doc.Close()

	if rg.IsEmpty() {
		printDocumentOutline(doc)
		return
	}
	printPages(doc, rg)
}

func printPages(doc *pdf.Document, rg Range) {
	for _, p := range rg.Pages(doc.GetCount()) {
		page, err := doc.GetPage(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Stdout.Write(page)
	}
}

func printDocumentOutline(doc *pdf.Document) {
	var print func(pdf.Outline, int)
	print = func(o pdf.Outline, level int) {
		fmt.Printf("%s%s", strings.Repeat(" ", level), o.Title)
		fmt.Println()
		for _, o := range o.Sub {
			print(o, level+1)
		}
	}
	for _, o := range doc.GetOutlines() {
		print(o, 0)
	}
}

var ErrInvalid = errors.New("invalid page number")

type Ranger interface {
	Pages(int64) []int
}

func makeInterval(from, to string) (Ranger, error) {
	fst, err := strconv.Atoi(from)
	if err != nil && from != "" {
		return nil, fmt.Errorf("%s: %w", from, ErrInvalid)
	}
	lst, err := strconv.Atoi(to)
	if err != nil && to != "" {
		return nil, fmt.Errorf("%s: %w", to, ErrInvalid)
	}
	if fst > 0 && lst > 0 && fst >= lst {
		return nil, fmt.Errorf("invalid interval (%d - %d)", fst, lst)
	}
	i := Interval{
		first: fst,
		last:  lst,
	}
	return i, nil
}

func (i Interval) Pages(n int64) []int {
	if i.first == 0 {
		i.first = 1
	}
	if i.last == 0 {
		i.last = int(n)
	}
	var ps []int
	for j := i.first; j <= i.last; j++ {
		ps = append(ps, j)
	}
	return ps
}

// a range is defined with
// : = all pages
// x: = from page X to end of document
// :x = from begin of a document to page X
// x:y = from page x to page y (offset can be negative)
// x,y,z = list of page
// possible to mix range and individual page
type Range struct {
	pages []Ranger
}

func (r *Range) Set(str string) (err error) {
	if str == "" {
		return nil
	}
	if str == ":" {
		r.pages = append(r.pages, all())
		return nil
	}
	r.pages, err = parseRange(str)
	return err
}

func (r *Range) String() string {
	if len(r.pages) == 0 {
		return "outline"
	}
	return "page"
}

func (r *Range) Pages(n int64) []int {
	var ps []int
	for _, p := range r.pages {
		ps = append(ps, p.Pages(n)...)
	}
	return ps
}

func (r *Range) IsEmpty() bool {
	return len(r.pages) == 0
}

const (
	colon = ':'
	comma = ','
)

func parseRange(str string) ([]Ranger, error) {
	var (
		pages []Ranger
		i     int
	)
	for j := 0; j < len(str); j++ {
		switch b := str[j]; b {
		case comma:
			g, err := makeSingle(str[i:j])
			if err != nil {
				return nil, err
			}
			pages, i = append(pages, g), j+1
		case colon:
			k := j + 1
			for ; k < len(str); k++ {
				if str[k] == comma {
					break
				}
				if str[k] == colon {
					return nil, fmt.Errorf("syntax error: unexpected colon")
				}
			}
			g, err := makeInterval(str[i:j], str[j+1:k])
			if err != nil {
				return nil, err
			}
			j = k + 1
			pages, i = append(pages, g), j
		default:
		}
	}
	if i < len(str) {
		g, err := makeSingle(str[i:])
		if err != nil {
			return nil, err
		}
		pages = append(pages, g)
	}
	return pages, nil
}

type Single struct {
	page int
}

func makeSingle(str string) (Ranger, error) {
	n, err := strconv.Atoi(str)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", str, ErrInvalid)
	}
	return Single{page: n}, nil
}

func (s Single) Pages(_ int64) []int {
	return []int{s.page}
}

type Interval struct {
	first int
	last  int
}

func all() Ranger {
	return Interval{}
}
