// Command openapi-gen writes the agent API description from its route table.
package main

import (
	"fmt"
	"os"

	"github.com/irashack/night-desk/internal/api"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: openapi-gen <output.json>")
		os.Exit(2)
	}
	data, err := api.OpenAPI()
	if err == nil {
		err = os.WriteFile(os.Args[1], append(data, '\n'), 0644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
