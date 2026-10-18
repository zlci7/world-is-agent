package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/dataroot"
	"gameagent/runtime/internal/gateway"
	"gameagent/runtime/internal/workerapi"
	"google.golang.org/grpc"
)

var version = "dev"

func main() {
	token := os.Getenv("WIA_WORKER_TOKEN")
	root := os.Getenv("WIA_WORKER_ROOT")
	if len(token) < 32 || root == "" || os.Getenv("WIA_MODEL_URL") == "" || os.Getenv("WIA_MODEL") == "" || os.Getenv("WIA_GAME_ID") == "" {
		log.Fatal("worker configuration is incomplete")
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0755); err != nil {
		log.Fatal("create worker data root failed")
	}
	var contextTokens, outputTokens int
	if err := json.Unmarshal([]byte(os.Getenv("WIA_MODEL_CONTEXT_TOKENS")), &contextTokens); err != nil || contextTokens < 1024 {
		log.Fatal("invalid context budget")
	}
	if err := json.Unmarshal([]byte(os.Getenv("WIA_MODEL_OUTPUT_TOKENS")), &outputTokens); err != nil || outputTokens < 1 || outputTokens >= contextTokens {
		log.Fatal("invalid output budget")
	}
	doc, _ := json.Marshal(map[string]any{"provider": "deepseek", "model": os.Getenv("WIA_MODEL"), "base_url": os.Getenv("WIA_MODEL_URL"), "api_key": "env:WIA_WORKER_TOKEN", "context_window_tokens": contextTokens, "max_output_tokens": outputTokens})
	if err := os.WriteFile(filepath.Join(root, "config", "model.json"), doc, 0600); err != nil {
		log.Fatal("write worker model configuration failed")
	}
	prefs := workerapi.NewPreferences()
	prefs.Refresh(context.Background(), os.Getenv("WIA_MODEL_URL"), token)
	runtime, err := bootstrap.OpenWithPlayerPreferences(root, dataroot.OS(), prefs.Read)
	if err != nil {
		log.Fatal("open worker data failed")
	}
	defer runtime.Close()
	if !runtime.Ready() {
		if err = runtime.SelectGame(os.Getenv("WIA_GAME_ID")); err != nil {
			log.Fatal("configure worker profile failed")
		}
	}
	if !runtime.Ready() {
		log.Fatal("worker is not ready")
	}
	if runtime.Snapshot().LoadedGame == nil || runtime.Snapshot().LoadedGame.ID != os.Getenv("WIA_GAME_ID") {
		log.Fatal("worker game does not match its deployment")
	}
	prefsCtx, cancelPrefs := context.WithCancel(context.Background())
	defer cancelPrefs()
	go prefs.Poll(prefsCtx, os.Getenv("WIA_MODEL_URL"), token)
	grpcServer := grpc.NewServer(grpc.StreamInterceptor(workerapi.StreamAuth(token)), grpc.MaxRecvMsgSize(2<<20))
	protocol.RegisterGameAgentGatewayServer(grpcServer, runtime)
	listener, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatal("worker gateway listen failed")
	}
	httpServer := &http.Server{Addr: ":8080", Handler: workerapi.Handler(runtime, token, version), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if err := grpcServer.Serve(listener); err != nil && err != grpc.ErrServerStopped {
			log.Print("worker gateway stopped")
		}
	}()
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Print("worker API stopped")
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	_ = httpServer.Close()
	_ = runtime.Close()
	gateway.ShutdownGRPC(grpcServer)
}
