// Chunks real documents with the notebook's own reader and splitter, so a corpus built
// for the polarity evaluation holds passages of exactly the shape the product stores.
//
//	go run ./spikes/04-polarity <dir of .html> > chunks.json
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/UncleSon21/vellatry/internal/notebook"
)

type passage struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Seq    int    `json:"seq"`
	Text   string `json:"text"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: polarity <dir of .html>")
		os.Exit(2)
	}
	files, err := filepath.Glob(filepath.Join(os.Args[1], "*.html"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "no .html in %s\n", os.Args[1])
		os.Exit(1)
	}
	var out []passage
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			continue
		}
		name := strings.TrimSuffix(filepath.Base(f), ".html")
		text := notebook.ReadHTML(body)
		chunks := notebook.Split(text)
		for _, c := range chunks {
			out = append(out, passage{
				ID:     fmt.Sprintf("%s-%03d", name, c.Seq),
				Source: name,
				Seq:    c.Seq,
				Text:   c.Text,
			})
		}
		fmt.Fprintf(os.Stderr, "%-34s %6d chars -> %3d chunks\n", name, len(text), len(chunks))
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "%d passages\n", len(out))
}
