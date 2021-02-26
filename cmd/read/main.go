package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/midbel/pdf"
)

// a range is defined with
// : = all pages
// x: = from page X to end of document
// :x = from begin of a document to page X
// x:y = from page x to page y (offset can be negative)
// x,y,z = list of page
// possible to mix range and individual page
type Range struct {
	page int
}

func (r *Range) Set(str string) (err error) {
	if str == ":" {
		return nil
	}
	r.page, err = strconv.Atoi(str)
	return err
}

func (r *Range) String() string {
	if r.page == 0 {
		return "outline"
	}
	return fmt.Sprintf("page #%d", r.page)
}

func (r *Range) Pages(n int64) []int {
	return []int{r.page}
}

func (r *Range) IsEmpty() bool {
	return r.page == 0
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
