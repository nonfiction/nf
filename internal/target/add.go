package target

import (
	"fmt"
	"io"
)

func RunAdd(argv []string, stderr io.Writer) int {
	if len(argv) < 2 {
		fmt.Fprintln(stderr, "target add takes provider and name: nf target add linode <name>")
		return 1
	}
	switch argv[0] {
	case "linode":
		return runAddLinode(argv[1:], stderr)
	default:
		fmt.Fprintf(stderr, "unsupported target provider %q\n", argv[0])
		return 1
	}
}
