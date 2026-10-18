// Package workerapi exposes the private, authenticated hosted-worker read surface.
package workerapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"gameagent/runtime/internal/bootstrap"
	"gameagent/runtime/internal/httpapi"
	"gameagent/runtime/internal/traceview"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func Authorized(value, token string) bool {
	return len(token) >= 32 && subtle.ConstantTimeCompare([]byte(value), []byte("Bearer "+token)) == 1
}

func StreamAuth(token string) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		md, _ := metadata.FromIncomingContext(stream.Context())
		values := md.Get("authorization")
		if len(values) != 1 || !Authorized(values[0], token) {
			return status.Error(codes.Unauthenticated, "worker_unauthenticated")
		}
		return handler(srv, stream)
	}
}

func Handler(runtime *bootstrap.Runtime, token, version string) http.Handler {
	reader := traceview.NewReader(runtime.Layout().TracePath(), traceview.Options{MaxBytes: traceview.DefaultMaxBytes, MaxTurns: 50})
	tasks := httpapi.TaskQueryHandler(runtime)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !Authorized(r.Header.Get("Authorization"), token) {
			http.Error(w, "unauthenticated", 401)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method_not_allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status":
			s := runtime.Snapshot()
			json.NewEncoder(w).Encode(map[string]any{"instance_id": s.InstanceID, "state": s.State, "ready": s.Ready, "reason_code": s.ReasonCode, "loaded_game": s.LoadedGame, "configured_game": s.ConfiguredGame, "restart_required": false, "adapters": s.Adapters, "connection_count": s.ConnectionCount, "last_connection_error": nil, "version": version})
		case "/task-worlds", "/tasks", "/tasks/detail":
			r2 := r.Clone(r.Context())
			u := *r.URL
			r2.URL = &u
			r2.URL.Path = "/api" + r.URL.Path
			tasks.ServeHTTP(w, r2)
		case "/turns":
			turns, err := reader.Turns()
			if err != nil {
				http.Error(w, `{"error":{"code":"trace_unreadable","message":"运行记录暂不可用"}}`, 503)
				return
			}
			for i := range turns {
				turns[i].Error = ""
				turns[i].AvailableTools = nil
			}
			json.NewEncoder(w).Encode(map[string]any{"instance_id": runtime.Snapshot().InstanceID, "turns": turns})
		default:
			http.NotFound(w, r)
		}
	})
}

func Bearer(ctx context.Context, token string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+strings.TrimSpace(token)))
}
