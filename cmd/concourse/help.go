package main

import (
	"bytes"
	"fmt"
	"go/doc"
	"os"
	"strconv"

	"github.com/alecthomas/kong"
	"golang.org/x/term"
)

// Use Kong's model in a stacked layout on narrow terminals. Concourse's long
// namespaced flags otherwise leave only a few characters for each help line.
func concourseHelp(options kong.HelpOptions, ctx *kong.Context) error {
	width, err := strconv.Atoi(os.Getenv("COLUMNS"))
	if err != nil || width <= 0 {
		width = 80
		if file, ok := ctx.Stdout.(*os.File); ok {
			if columns, _, err := term.GetSize(int(file.Fd())); err == nil && columns > 0 {
				width = columns
			}
		}
	}
	selected := ctx.Selected()
	if width >= 60 || selected == nil {
		return kong.DefaultHelpPrinter(options, ctx)
	}
	if width < 24 {
		width = 24
	}
	var out bytes.Buffer
	wrap := func(text, indent string) {
		doc.ToText(&out, text, indent, indent, width-len(indent))
	}
	wrap("Usage: "+selected.FullPath()+" [flags] [<args> ...]", "")
	fmt.Fprintln(&out)
	wrap(selected.Help, "")
	group := ""
	for _, flags := range selected.AllFlags(true) {
		for _, flag := range flags {
			title := "Flags"
			if flag.Group != nil && flag.Group.Title != "" {
				title = flag.Group.Title
			}
			if title != group {
				fmt.Fprintf(&out, "\n%s:\n", title)
				group = title
			}
			fmt.Fprintf(&out, "  %s\n", flag.String())
			help := kong.DefaultHelpValueFormatter(flag.Value)
			if flag.Required {
				help = "Required. " + help
			}
			wrap(help, "    ")
			fmt.Fprintln(&out)
		}
	}
	_, err = ctx.Stdout.Write(out.Bytes())
	return err
}
