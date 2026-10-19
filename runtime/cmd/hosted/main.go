package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gameagent/console"
	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/gateway"
	"gameagent/runtime/internal/hosted"
	"google.golang.org/grpc"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	configPath := flag.String("config", "hosted.json", "hosted service configuration")
	command := flag.String("command", "serve", "serve, invite, init-admin, reset-password, disable, grant-admin, revoke-admin, backup-accounts, or deployment-id")
	inviteHours := flag.Int("invite-hours", 72, "one-use invitation lifetime in hours")
	email := flag.String("email", "", "account email for private password recovery")
	backupPath := flag.String("backup-path", "", "new account database backup destination")
	flag.Parse()
	c, err := hosted.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(c.DataRoot, 0755); err != nil {
		return err
	}
	if *command == "serve" {
		unlock, err := hosted.Lock(filepath.Join(c.DataRoot, "controller.lock"))
		if err != nil {
			return fmt.Errorf("another controller owns this deployment")
		}
		defer unlock()
	}
	store, err := hosted.OpenAccounts(c)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := context.Background()
	switch *command {
	case "deployment-id":
		fmt.Println(hosted.DeploymentID(store))
		return nil
	case "invite":
		secret, err := store.Invite(ctx, time.Duration(*inviteHours)*time.Hour)
		if err == nil {
			fmt.Println(secret)
		}
		return err
	case "reset-password", "init-admin":
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 514))
		if err != nil {
			return err
		}
		password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		if *command == "init-admin" {
			return store.BootstrapAdmin(ctx, *email, password)
		}
		return store.ResetPassword(ctx, *email, password)
	case "disable":
		return store.Disable(ctx, *email)
	case "grant-admin":
		return store.SetAdmin(ctx, *email, true)
	case "revoke-admin":
		return store.SetAdmin(ctx, *email, false)
	case "backup-accounts":
		return store.Backup(ctx, *backupPath)
	case "serve":
	default:
		return fmt.Errorf("unknown command")
	}
	if os.Getenv(c.ModelKeyEnv) == "" {
		return fmt.Errorf("platform model credential is missing")
	}
	if err = store.RecoverPendingCalls(ctx); err != nil {
		return err
	}
	workers := hosted.NewDockerWorkers(c, store)
	server := hosted.New(c, store, workers, console.Assets())
	grpcServer := grpc.NewServer(grpc.MaxRecvMsgSize(2<<20), grpc.MaxConcurrentStreams(32))
	protocol.RegisterGameAgentGatewayServer(grpcServer, server)
	listener, err := net.Listen("tcp", c.GRPCAddr)
	if err != nil {
		return err
	}
	defer listener.Close()
	web := &http.Server{Addr: c.HTTPAddr, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	model := &http.Server{Addr: c.ModelAddr, Handler: server.ModelHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: time.Duration(c.ModelTimeoutSeconds+10) * time.Second, MaxHeaderBytes: 16 << 10}
	failures := make(chan error, 3)
	go func() { failures <- grpcServer.Serve(listener) }()
	go func() { failures <- web.ListenAndServe() }()
	go func() { failures <- model.ListenAndServe() }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	log.Print("WIA hosted service started")
	select {
	case <-stop:
	case <-failures:
		log.Print("hosted listener stopped")
	}
	server.Close()
	_ = web.Close()
	_ = model.Close()
	gateway.ShutdownGRPC(grpcServer)
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return workers.Close(closeCtx)
}
