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
	for _, f := range doc.GetFonts() {
		printFont(f)
	}
}

const row = "%-8s | %-36s | %-24s | %-24s | %5t | 0x%02x - 0x%02x"

func printFont(f pdf.Font) {
	fmt.Printf(row, f.Name, f.Base, f.Sub, f.Encoding, f.Unicode, f.First, f.Last)
	fmt.Println()
}
