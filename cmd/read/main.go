package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/midbel/pdf"
)

type Ranger interface {
	Pages() []int
}

type Single struct {
	page int
}

func makeSingle(str string) (Ranger, error) {
	n, err := strconv.Atoi(str)
	if err != nil {
		return nil, err
	}
	return Single{page: n}, nil
}

func (s Single) Pages() []int {
	return []int{s.page}
}

type Interval struct {
	first int
	last  int
}

func makeInterval(from, to string) (Ranger, error) {
	fst, err := strconv.Atoi(from)
	if err != nil {
		return nil, err
	}
	lst, err := strconv.Atoi(to)
	if err != nil {
		return nil, err
	}
	if fst >= last {
		return nil, fmt.Errorf("invalid interval (%d - %d)", fst, lst)
	}
	return Interval{first: fst, last: lst}, nil
}

func (i Interval) Pages() []int {
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

func (r *Range) Set(str string) error {
	var i int
	for j, b := range str {
		switch b {
		case ',':
			g, err := makeSingle(str[i:j])
			if err != nil {
				return err
			}
			i = j + 1
			r.pages = append(r.pages, g)
		case ':':
			k := j+1
			for ; k < len(str); k++ {
				if str[k] == ',' {
					break
				}
				if str[k] == ':' {
					return fmt.Errorf("syntax error: unexpected colon")
				}
			}
			g, err := makeInterval(str[i:j], str[j+1:k])
			if err != nil {
				return err
			}
			r.pages = append(r.pages, g)
			i = k+1
		default:
		}
	}
	if i > 0 && i < len(str) {
		g, err := makeSingle(str[i:])
		if err != nil {
			return err
		}
		r.pages = append(r.pages, g)
	}
	return nil
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
		ps = append(ps, p.Pages()...)
	}
	return ps
}

func (r *Range) IsEmpty() bool {
	return len(r.pages) == 0
}

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
