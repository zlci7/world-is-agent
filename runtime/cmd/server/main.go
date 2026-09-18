package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"

	"google.golang.org/grpc"
)

func main() {
	dataRoot := flag.String(dataroot.FlagName, "", "runtime data root; overrides the "+dataroot.EnvName+" environment variable")
	flag.Parse()

	// Bootstrap always succeeds for a writable data root. The agent core is the
	// part that needs configuration, and its state is reported rather than fatal:
	// the process has to stay alive for the user to be able to configure it.
	runtime, err := bootstrap.Open(*dataRoot, dataroot.OS())
	if err != nil {
		log.Fatalf("open runtime failed: %v", err)
	}
	defer runtime.Close()
	log.Printf("GameAgent data root: %s", runtime.Layout().Root())
	if runtime.State() == bootstrap.StateReady {
		log.Printf("GameAgent agent core ready: model config %s", runtime.ModelConfigPath())
	} else {
		log.Printf("GameAgent agent core is not ready (%s): %s", runtime.State(), runtime.Reason())
	}
	if root := runtime.AgentConfig().DefinitionCatalogRoot; root != "" {
		log.Printf("GameAgent definition catalog root: %s", root)
	}

	process, err := newGatewayRuntime(context.Background(), runtime)
	if err != nil {
		log.Fatalf("open task runtime failed: %v", err)
	}

	grpcServer := grpc.NewServer()
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, process.gateway)

	listener, err := net.Listen("tcp", "127.0.0.1:50051")
	if err != nil {
		log.Fatalf("listen failed: %v", err)
	}

	go func() {
		log.Println("GameAgent Runtime listening on 127.0.0.1:50051")
		if err := grpcServer.Serve(listener); err != nil && err != grpc.ErrServerStopped {
			log.Printf("serve stopped: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	<-stop

	log.Println("shutting down GameAgent Runtime")
	if err := process.shutdown(context.Background(), grpcServer); err != nil {
		log.Printf("shutdown task runtime: %v", err)
	}
}
