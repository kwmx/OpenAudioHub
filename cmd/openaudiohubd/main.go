package main

import (
	"flag"
	"fmt"
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
	if *listen != "" {
		app.SetListen(*listen)
	}
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}

	_ = os.Stdout
}
