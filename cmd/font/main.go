package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/midbel/pdf"
)

func main() {
	flag.Parse()

	doc, err := pdf.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer doc.Close()

	doc.Walk(func(o pdf.Object) bool {
		if o.IsFont() {
			printFont(o)
		}
		return true
	})
}

func printFont(o pdf.Object) {
	var (
		name = o.GetString("name")
		base = o.GetString("basefont")
		sub  = o.GetString("subtype")
		enco = o.GetString("encoding")
		unic = o.Has("tounicode")
	)
	fmt.Printf("%7s | %-8s | %-36s | %-16s | %-16s | %t\n", o.Oid, name, base, sub, enco, unic)
}
