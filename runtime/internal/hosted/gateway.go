package hosted

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/accounts"
	"gameagent/runtime/internal/workerapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type connection struct {
	device string
	key    string
	cancel context.CancelFunc
	done   chan struct{}
}

func compatibleVersion(value, minimum string) bool {
	parse := func(value string) ([3]int, bool) {
		var result [3]int
		parts := strings.Split(value, ".")
		if len(parts) != 3 {
			return result, false
		}
		for i, part := range parts {
			if part == "" || strings.Trim(part, "0123456789") != "" {
				return result, false
			}
			n, err := strconv.Atoi(part)
			if err != nil {
				return result, false
			}
			result[i] = n
		}
		return result, true
	}
	a, ok := parse(value)
	if !ok {
		return false
	}
	b, ok := parse(minimum)
	if !ok {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

func (s *Server) Connect(down protocol.GameAgentGateway_ConnectServer) error {
	md, _ := metadata.FromIncomingContext(down.Context())
	auth, devices := md.Get("authorization"), md.Get("x-wia-device-id")
	if len(auth) != 1 || len(devices) != 1 || !strings.HasPrefix(auth[0], "Bearer ") {
		return status.Error(codes.Unauthenticated, "unauthenticated")
	}
	secret := strings.TrimPrefix(auth[0], "Bearer ")
	device := devices[0]
	user, key, err := s.Accounts.AuthenticateKey(down.Context(), secret, device, false)
	if err != nil {
		if errors.Is(err, accounts.ErrCredentials) {
			return status.Error(codes.Unauthenticated, "unauthenticated")
		}
		return status.Error(codes.Unavailable, "authentication_unavailable")
	}
	type firstResult struct {
		message *protocol.AdapterMessage
		err     error
	}
	first := make(chan firstResult, 1)
	go func() { m, e := down.Recv(); first <- firstResult{m, e} }()
	var hello *protocol.AdapterMessage
	select {
	case result := <-first:
		if result.err != nil {
			return result.err
		}
		hello = result.message
	case <-time.After(10 * time.Second):
		return status.Error(codes.DeadlineExceeded, "hello_timeout")
	case <-down.Context().Done():
		return down.Context().Err()
	}
	if hello.GetHello() == nil || hello.GetHello().GetGameId() != s.Config.GameID || hello.GetHello().GetProtocolVersion() != "v1alpha2" || !compatibleVersion(hello.GetHello().GetAdapterVersion(), s.Config.MinAdapterVersion) {
		return status.Error(codes.FailedPrecondition, "unsupported_adapter")
	}
	if _, _, err = s.Accounts.AuthenticateKey(down.Context(), secret, device, true); err != nil {
		return status.Error(codes.Unauthenticated, "unauthenticated")
	}
	ctx, cancel := context.WithCancel(down.Context())
	defer cancel()
	c := &connection{device: device, key: key, cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	old := s.connections[user]
	if old != nil && old.device != device {
		s.mu.Unlock()
		return status.Error(codes.AlreadyExists, "account_in_use")
	}
	if old != nil {
		old.cancel()
	}
	s.connections[user] = c
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		if s.connections[user] == c {
			delete(s.connections, user)
			if modelCancel := s.modelCancels[user]; modelCancel != nil {
				modelCancel()
			}
		}
		close(c.done)
		s.mu.Unlock()
	}()
	if old != nil {
		select {
		case <-old.done:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return status.Error(codes.Unavailable, "previous_connection_draining")
		}
	}
	a, err := s.Workers.Ensure(ctx, user)
	if err != nil {
		if workerRecoveryBlocked(err) {
			return status.Error(codes.FailedPrecondition, "worker_recovery_blocked")
		}
		if errors.Is(err, ErrWorkerCapacity) {
			return status.Error(codes.ResourceExhausted, "service_busy")
		}
		return status.Error(codes.Unavailable, "worker_unavailable")
	}
	client, err := grpc.NewClient(a.GRPC, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(2<<20)))
	if err != nil {
		return status.Error(codes.Unavailable, "worker_unavailable")
	}
	defer client.Close()
	up, err := protocol.NewGameAgentGatewayClient(client).Connect(workerapi.Bearer(ctx, a.Token))
	if err != nil {
		return status.Error(codes.Unavailable, "worker_unavailable")
	}
	defer up.CloseSend()
	if err = up.Send(hello); err != nil {
		return status.Error(codes.Unavailable, "worker_unavailable")
	}
	results := make(chan error, 2)
	go func() {
		for {
			message, e := down.Recv()
			if e != nil {
				results <- e
				return
			}
			if e = up.Send(message); e != nil {
				results <- e
				return
			}
		}
	}()
	go func() {
		for {
			message, e := up.Recv()
			if e != nil {
				results <- e
				return
			}
			if e = down.Send(message); e != nil {
				results <- e
				return
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case e := <-results:
			if errors.Is(e, io.EOF) {
				return nil
			}
			if status.Code(e) == codes.Unauthenticated {
				return status.Error(codes.Unauthenticated, "unauthenticated")
			}
			return status.Error(codes.Unavailable, "connection_interrupted")
		case <-ctx.Done():
			return status.Error(codes.Canceled, "connection_closed")
		case <-ticker.C:
			if err = s.Accounts.KeyActive(ctx, key); err != nil {
				return status.Error(codes.Unauthenticated, "key_revoked")
			}
		}
	}
}

func (s *Server) disconnectKey(user, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.connections[user]; c != nil && c.key == key {
		c.cancel()
		if cancel := s.modelCancels[user]; cancel != nil {
			cancel()
		}
	}
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.connections {
		c.cancel()
	}
	for _, cancel := range s.modelCancels {
		cancel()
	}
}
