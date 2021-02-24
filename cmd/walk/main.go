package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/midbel/hexdump"
	"github.com/midbel/pdf"
)

func main() {
	var (
		all = flag.Bool("a", false, "all")
		raw = flag.Bool("r", false, "raw")
	)
	flag.Parse()
	doc, err := pdf.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer doc.Close()

	doc.Walk(func(o pdf.Object) bool {
		printObject(o, *raw)
		if *all {
			for _, o := range o.GetEmbeddedObjects() {
				printObject(o, *raw)
			}
		}
		return true
	})
}

func printObject(o pdf.Object, raw bool) {
	if !o.Dict.IsEmpty() {
		fmt.Printf("%s %+v", o.Oid, o.Dict)
	} else if o.Data != nil {
		fmt.Printf("%s %+v", o.Oid, o.Data)
	} else {
		return
	}
	fmt.Println()
	if raw && len(o.Content) > 0 {
		body, err := o.Body()
		if err != nil {
			return
		}
		fmt.Println(hexdump.Dump(body))
	}
}
