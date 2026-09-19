package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"gameagent/console"
	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/browser"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/httpapi"

	"google.golang.org/grpc"
)

// version is reported to the local client. A development build has no release to
// name, so it reports "dev"; the release script reads VERSION and sets this at
// link time, which is the only build that carries a real version.
var version = "dev"

// defaultGRPCAddr is where adapters connect. It is an interface with the games
// that already install adapters, so the default stays fixed; the flag exists so a
// second Runtime, or one alongside something else on that port, can still start.
const defaultGRPCAddr = "127.0.0.1:50051"

func main() {
	dataRoot := flag.String(dataroot.FlagName, "", "runtime data root; overrides the "+dataroot.EnvName+" environment variable")
	httpAddr := flag.String("http-addr", "127.0.0.1:0", "local control plane address; must be a loopback address, port 0 picks a free port")
	grpcAddr := flag.String("grpc-addr", defaultGRPCAddr, "address adapters connect to")
	noOpen := flag.Bool("no-open", false, "do not open the browser at the local client")
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

	// The control plane starts before the adapter can connect, and keeps working
	// while the agent core is not ready. That is the whole point of separating
	// bootstrap from the core: the client is how the user finds out what is
	// missing, so it cannot depend on the core being configured.
	controlPlane := startControlPlane(*httpAddr, runtime, *grpcAddr, *noOpen)
	defer func() {
		if controlPlane != nil {
			_ = controlPlane.Shutdown()
		}
	}()

	process, err := newGatewayRuntime(context.Background(), runtime)
	if err != nil {
		log.Fatalf("open task runtime failed: %v", err)
	}

	grpcServer := grpc.NewServer()
	protocolv1alpha2.RegisterGameAgentGatewayServer(grpcServer, process.gateway)

	listener, err := net.Listen("tcp", *grpcAddr)
	if err != nil {
		log.Fatalf("listen failed: %v", err)
	}

	go func() {
		log.Printf("GameAgent Runtime listening on %s", *grpcAddr)
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

// startControlPlane serves the local client. A control plane that cannot start is
// reported and not fatal: the game is still playable only through the gRPC
// adapter, so refusing to run would turn a missing UI into a broken game.
func startControlPlane(addr string, runtime *bootstrap.Runtime, grpcAddr string, noOpen bool) *httpapi.Server {
	server, err := httpapi.New(httpapi.Options{
		Addr:     addr,
		Runtime:  runtime,
		Assets:   console.Assets(),
		GRPCAddr: grpcAddr,
		Version:  version,
		Logger:   log.Default(),
	})
	if err != nil {
		log.Printf("GameAgent local client unavailable: %v", err)
		return nil
	}

	go func() {
		if err := server.Serve(); err != nil {
			log.Printf("local client stopped: %v", err)
		}
	}()

	log.Printf("GameAgent local client: %s", server.URL())
	if noOpen {
		log.Printf("open this URL to hand the session to your browser: %s", server.BrowserURL())
		return server
	}
	if err := browser.Open(server.BrowserURL()); err != nil {
		// Losing the automatic handover is recoverable: the URL above carries the
		// same token, so the user can still open it.
		log.Printf("open browser failed: %v; open this URL instead: %s", err, server.BrowserURL())
	}
	return server
}
