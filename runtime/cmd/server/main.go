package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	protocolv1alpha2 "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/llm"
	"gameagent/runtime/internal/trace"

	"google.golang.org/grpc"
)

func main() {
	modelProvider, modelConfig, err := llm.NewProviderFromConfigFile(llm.ConfigPathFromEnv())
	if err != nil {
		log.Fatalf("load model provider failed: %v", err)
	}
	log.Printf("GameAgent model provider: %s model=%s", modelConfig.Provider, modelConfig.Model)

	// 初始化 trace recorder。
	var traceRecorder trace.Recorder
	traceRecorder, err = trace.NewJSONLRecorder(
		// MVP0 trace path 依赖从仓库根目录作为 cwd 启动；后续由 agent.json 的 trace.path 接管。
		"runtime/.local/traces.jsonl",
		trace.JSONLRecorderOptions{},
	)
	if err != nil {
		log.Printf("create trace recorder failed: %v, fallback to noop", err)
		traceRecorder = trace.NoopRecorder{}
	}
	defer traceRecorder.Close(context.Background())

	agentConfig, err := agent.LoadConfigFile(agent.ConfigPathFromEnv())
	if err != nil {
		log.Fatalf("load agent config failed: %v", err)
	}
	log.Printf(
		"GameAgent agent config: turn=%s llm=%s observe=%s action=%s",
		agentConfig.TurnTimeout,
		agentConfig.LLMTimeout,
		agentConfig.ObserveTimeout,
		agentConfig.ActionTimeout,
	)

	var definitionCatalog definition.Catalog
	if agentConfig.DefinitionCatalogRoot != "" {
		definitionCatalog, err = definition.LoadCatalogFromDir(agentConfig.DefinitionCatalogRoot)
		if err != nil {
			log.Fatalf("load definition catalog failed: %v", err)
		}
		log.Printf("GameAgent definition catalog root: %s", agentConfig.DefinitionCatalogRoot)
	}

	agentLoop := agent.NewLoop(modelProvider, traceRecorder, agentConfig, agent.WithDefinitionCatalog(definitionCatalog))

	process, err := newGatewayRuntime(context.Background(), agentLoop, agentConfig.Task)
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
