package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if err := runConcourse(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func runConcourse(args []string, stdout io.Writer) error {
	var cmd ConcourseCommand
	parser, err := newConcourseParser(&cmd, stdout)
	if err != nil {
		return err
	}
	command, remaining, err := parser.parse(args)
	if err != nil || command == nil {
		return err
	}
	// Preserve Concourse's environment cleanup before starting the real service.
	_ = os.Unsetenv("CONCOURSE_CONFIG")
	return parser.schema.CommandHandler(command, remaining)
}
