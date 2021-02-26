package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/midbel/pdf"
)

const timePattern = "2006-01-02 15:04:05"

func main() {
	flag.Parse()
	doc, err := pdf.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer doc.Close()

	info := doc.GetDocumentInfo()
	printLine("version", "PDF-"+doc.GetVersion())
	printLine("title", info.Title)
	printLine("language", doc.GetLang())
	printLine("author", info.Author)
	printLine("subject", info.Subject)
	if !info.Created.IsZero() {
		printLine("created", info.Created.Format(timePattern))
	}
	if !info.Modified.IsZero() {
		printLine("modified", info.Modified.Format(timePattern))
	}
	if len(info.Keywords) > 0 {
		printLine("keywords", strings.Join(info.Keywords, ", "))
	}
	for _, sig := range doc.GetSignatures() {
		if sig.When.IsZero() {
			printLine("signed by", sig.Who)
		} else {
			printLine("signed by", fmt.Sprintf("%s (%s)", sig.Who, sig.When.Format(timePattern)))
		}
	}
	printLine("pages", strconv.FormatInt(doc.GetCount(), 10))
}

func printValue(key string, value pdf.Value) {
	if value == nil {
		return
	}
	fmt.Printf("%-12s: %s", strings.Title(key), value)
	fmt.Println()
}

func printLine(key, value string) {
	if value == "" {
		return
	}
	fmt.Printf("%-12s: %s", strings.Title(key), value)
	fmt.Println()
}
