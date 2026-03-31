package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		serveCmd := flag.NewFlagSet("serve", flag.ExitOnError)
		listen := serveCmd.String("listen", envOr("LISTEN_ADDR", ":8081"), "HTTP listen address (e.g. :8081)")
		_ = serveCmd.Parse(os.Args[2:])
		if err := runServer(*listen); err != nil {
			fmt.Fprintf(os.Stderr, "server: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Accept input from arg or stdin
	var input string
	if len(os.Args) > 1 {
		input = strings.Join(os.Args[1:], " ")
	} else {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		input = scanner.Text()
	}

	if strings.TrimSpace(input) == "" {
		fmt.Fprintln(os.Stderr, "error: no input provided")
		os.Exit(1)
	}

	cmd, err := translate(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(cmd)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
