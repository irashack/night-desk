package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/irashack/night-desk/internal/lean"
)

const ratingsUsage = "usage: nightdesk ratings import --name NAME --url URL --license LICENSE --attribution TEXT --out FILE [--force] <ratings.csv>"

// ratingsCommand turns an outlet,domain,lean[,factuality] CSV into the ratings
// file lean.ratings_file reads, validated by the same rules as loading.
func ratingsCommand(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] != "import" {
		fmt.Fprintln(errOut, ratingsUsage)
		return 2
	}
	flags := flag.NewFlagSet("ratings import", flag.ContinueOnError)
	flags.SetOutput(errOut)
	var src lean.Source
	flags.StringVar(&src.Name, "name", "", "ratings source name")
	flags.StringVar(&src.URL, "url", "", "ratings source URL")
	flags.StringVar(&src.License, "license", "", "ratings licence")
	flags.StringVar(&src.Attribution, "attribution", "", "attribution shown wherever a rating appears")
	path := flags.String("out", "", "ratings file to write")
	force := flags.Bool("force", false, "replace an existing ratings file")
	if flags.Parse(args[1:]) != nil {
		return 2
	}
	if flags.NArg() != 1 || *path == "" {
		fmt.Fprintln(errOut, ratingsUsage)
		return 2
	}
	f, err := os.Open(flags.Arg(0))
	if err != nil {
		fmt.Fprintln(errOut, "ratings CSV unavailable")
		return 1
	}
	ratings, problems, err := lean.ImportCSV(f, src)
	f.Close()
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(errOut, p)
		}
		return 1
	}
	b, err := json.MarshalIndent(ratings, "", "  ")
	if err != nil {
		fmt.Fprintln(errOut, "ratings encoding failed")
		return 1
	}
	mode := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if *force {
		mode = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	w, err := os.OpenFile(*path, mode, 0o644)
	if errors.Is(err, os.ErrExist) {
		fmt.Fprintln(errOut, "ratings file exists; use --force to replace it")
		return 1
	}
	if err != nil {
		fmt.Fprintln(errOut, "ratings file could not be written")
		return 1
	}
	_, err = w.Write(append(b, '\n'))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fmt.Fprintln(errOut, "ratings file could not be written")
		return 1
	}
	fmt.Fprintf(out, "Wrote %d outlet ratings.\n", len(ratings.Ratings))
	return 0
}
