package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/openaudiohub/openaudiohub/internal/oah"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/openaudiohub/config.json", "configuration file")
	listen := flag.String("listen", "", "override listen address")
	initConfig := flag.Bool("init", false, "initialize configuration from password on stdin")
	showVersion := flag.Bool("version", false, "print version and exit")
	doctor := flag.Bool("doctor", false, "check the hub for problems and exit")
	fix := flag.Bool("fix", false, "with -doctor, apply every available fix")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if *initConfig {
		if err := oah.InitConfig(*configPath); err != nil {
			log.Fatal(err)
		}
		fmt.Println("OpenAudioHub configuration initialized")
		return
	}

	app, err := oah.NewApp(*configPath, version)
	if err != nil {
		log.Fatal(err)
	}
	if *doctor {
		rep := app.RunDoctor(*fix)
		printDoctor(os.Stdout, rep, *fix)
		if rep.Problems > 0 {
			os.Exit(1)
		}
		return
	}
	if *listen != "" {
		app.SetListen(*listen)
	}
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func printDoctor(w io.Writer, rep oah.DoctorReport, fixed bool) {
	mark := map[string]string{"ok": " ok ", "warning": "WARN", "problem": "FAIL", "skipped": "skip"}
	fmt.Fprintln(w, "OpenAudioHub doctor")
	for _, c := range rep.Checks {
		fmt.Fprintf(w, "[%s] %s: %s\n", mark[c.Status], c.Title, c.Detail)
		switch {
		case c.Fixed:
			fmt.Fprintln(w, "       fixed.", c.FixNote)
		case c.FixError != "":
			fmt.Fprintln(w, "       fix failed:", c.FixError)
		case c.FixLabel != "":
			fmt.Fprintf(w, "       fix available: %s\n", c.FixLabel)
		}
	}
	fmt.Fprintf(w, "\n%d problems, %d warnings", rep.Problems, rep.Warnings)
	if fixed {
		fmt.Fprintf(w, ", %d fixed", rep.Fixed)
	}
	fmt.Fprintln(w, ".")
	if !fixed && rep.Fixable > 0 {
		fmt.Fprintf(w, "Run again with -fix to apply %d fixes.\n", rep.Fixable)
	}
}
