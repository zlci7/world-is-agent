package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gameagent/console"
	"gameagent/runtime/internal/browser"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/storyapi"
	"gameagent/runtime/internal/storyapp"
)

var version = "dev"

func main() {
	dataRootFlag := flag.String(dataroot.FlagName, "", "World Is Agent data root")
	httpAddr := flag.String("http-addr", "127.0.0.1:0", "local browser address; must be loopback")
	modelConfig := flag.String("model-config", "", "optional model configuration path")
	noOpen := flag.Bool("no-open", false, "do not open the browser automatically")
	flag.Parse()

	root, err := dataroot.Resolve(*dataRootFlag, dataroot.OS())
	if err != nil {
		log.Fatalf("resolve data root: %v", err)
	}
	app, err := storyapp.Open(context.Background(), storyapp.Options{DataRoot: root, ModelConfigPath: *modelConfig, UserID: storyapp.LocalUserID, Logger: log.Default()})
	if err != nil {
		log.Fatalf("open World Is Agent: %v", err)
	}
	defer func() {
		if err := app.Close(); err != nil {
			log.Printf("close World Is Agent: %v", err)
		}
	}()

	server, err := storyapi.New(storyapi.Options{Addr: *httpAddr, App: app, Assets: console.Assets(), Version: version, Logger: log.Default()})
	if err != nil {
		log.Fatalf("start local story service: %v", err)
	}
	defer server.Shutdown()
	go func() {
		if err := server.Serve(); err != nil {
			log.Printf("story service stopped: %v", err)
		}
	}()

	log.Printf("World Is Agent data root: %s", root)
	log.Printf("World Is Agent local client: %s", server.BrowserURL())
	if !*noOpen {
		if err := browser.Open(server.BrowserURL()); err != nil {
			log.Printf("open browser failed: %v; open this URL instead: %s", err, server.BrowserURL())
		}
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down World Is Agent")
}
