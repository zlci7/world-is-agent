package main

import (
	"context"
	"errors"

	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/gateway"
	"gameagent/runtime/internal/task"
	"google.golang.org/grpc"
)

type gatewayRuntime struct {
	gateway *gateway.Server
	store   *task.SQLiteStore
}

func newGatewayRuntime(ctx context.Context, core *bootstrap.Runtime) (*gatewayRuntime, error) {
	config := core.AgentConfig().Task.WithDefaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	process := &gatewayRuntime{}
	var options []gateway.ServerOption
	if config.Enabled {
		storeOptions := config.StoreOptions
		storeOptions.Path = config.DBPath
		store, err := task.OpenSQLiteStore(ctx, storeOptions)
		if err != nil {
			return nil, err
		}
		process.store = store
		options = append(options, gateway.WithTaskService(task.NewService(store)), gateway.WithTaskResultHistory(core.HistoryStore()))
	}
	process.gateway = gateway.NewServer(core, options...)
	if config.Enabled {
		dispatch := task.DispatcherConfig{ScanInterval: config.ScanInterval, BatchSize: config.DispatchBatch, RetryMin: config.RetryMin, RetryMax: config.RetryMax}
		if err := process.gateway.StartTaskDispatcher(ctx, dispatch, nil, nil); err != nil {
			_ = process.store.Close()
			return nil, err
		}
	}
	return process, nil
}

func (p *gatewayRuntime) shutdown(ctx context.Context, transport *grpc.Server) error {
	p.gateway.StopTaskAdmission()
	err := p.gateway.Close(ctx)
	gateway.ShutdownGRPC(transport)
	if p.store != nil {
		err = errors.Join(err, p.store.Close())
	}
	return err
}
