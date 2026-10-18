package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"gameagent/runtime/internal/accounts"
)

type Assignment struct {
	UserID     string
	Generation string
	Token      string
	GRPC       string
	API        string
}

type Workers interface {
	Ensure(context.Context, string) (Assignment, error)
	Authenticate(context.Context, string) (Assignment, error)
	Close(context.Context) error
}

var (
	ErrWorkerCapacity        = errors.New("service_busy")
	ErrWorkerDataMissing     = errors.New("worker_data_missing")
	ErrWorkerOwnership       = errors.New("worker_ownership_conflict")
	ErrWorkerVolumeOwnership = errors.New("worker_volume_ownership_conflict")
)

func workerRecoveryBlocked(err error) bool {
	return errors.Is(err, ErrWorkerDataMissing) || errors.Is(err, ErrWorkerOwnership) || errors.Is(err, ErrWorkerVolumeOwnership)
}

// DockerWorkers owns fixed-name containers and volumes on one deployment host.
// The controller is the only writer of this allocation map; Docker's unique names
// and the process-level deployment lock also cover controller restarts.
type DockerWorkers struct {
	mu          sync.Mutex
	Config      Config
	Accounts    *accounts.Store
	allocations map[string]Assignment
	closed      bool
	run         func(context.Context, ...string) ([]byte, error)
	probe       func(context.Context, Assignment) error
}

func NewDockerWorkers(c Config, s *accounts.Store) *DockerWorkers {
	return &DockerWorkers{Config: c, Accounts: s, allocations: map[string]Assignment{}, run: docker, probe: probeWorker}
}

func DeploymentID(s *accounts.Store) string { return s.Digest("deployment")[:24] }

func probeWorker(ctx context.Context, a Assignment) error {
	req, err := http.NewRequestWithContext(ctx, "GET", a.API+"/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var state struct {
		Ready bool `json:"ready"`
	}
	if json.NewDecoder(response.Body).Decode(&state) != nil || response.StatusCode != 200 || !state.Ready {
		return errors.New("worker_not_ready")
	}
	return nil
}

func docker(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("container operation %s failed", args[0])
	}
	return output, nil
}

type dockerState struct {
	ID     string
	State  struct{ Running bool }
	Config struct {
		Image  string
		Labels map[string]string
	}
}

func (d *DockerWorkers) Ensure(ctx context.Context, user string) (Assignment, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || !safeID.MatchString(user) {
		return Assignment{}, errors.New("worker_unavailable")
	}
	if current, ok := d.allocations[user]; ok {
		if d.probe(ctx, current) == nil {
			return current, nil
		}
		delete(d.allocations, user)
	}
	generation, err := d.Accounts.Generation(ctx, user)
	if err != nil {
		return Assignment{}, err
	}
	name := "wia-" + user
	a := Assignment{UserID: user, Generation: generation, Token: d.Accounts.Digest("worker:" + user + ":" + generation), GRPC: name + ":50051", API: "http://" + name + ":8080"}
	deployment := DeploymentID(d.Accounts)
	list, err := d.run(ctx, "ps", "-a", "--filter", "label=wia.deployment="+deployment, "--format", "{{.Names}}")
	if err != nil {
		return Assignment{}, err
	}
	names := strings.Fields(string(list))
	exists := false
	for _, candidate := range names {
		if candidate == name {
			exists = true
		}
	}
	if !exists && len(names) >= d.Config.MaxWorkers {
		return Assignment{}, ErrWorkerCapacity
	}
	initialized, err := d.Accounts.VolumeInitialized(ctx, user)
	if err != nil {
		return Assignment{}, err
	}
	volumes, err := d.run(ctx, "volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return Assignment{}, err
	}
	volumeExists := false
	for _, volume := range strings.Fields(string(volumes)) {
		if volume == name+"-data" {
			volumeExists = true
		}
	}
	if initialized && !volumeExists {
		return Assignment{}, ErrWorkerDataMissing
	}
	if !volumeExists {
		if _, err = d.run(ctx, "volume", "create", "--label", "wia.managed=1", "--label", "wia.deployment="+deployment, "--label", "wia.user="+user, name+"-data"); err != nil {
			return Assignment{}, err
		}
	} else {
		data, err := d.run(ctx, "volume", "inspect", name+"-data")
		if err != nil {
			return Assignment{}, err
		}
		var volumes []struct{ Labels map[string]string }
		if json.Unmarshal(data, &volumes) != nil || len(volumes) != 1 || volumes[0].Labels["wia.user"] != user || volumes[0].Labels["wia.deployment"] != deployment {
			return Assignment{}, ErrWorkerVolumeOwnership
		}
	}
	if !initialized {
		if err = d.Accounts.MarkVolumeInitialized(ctx, user); err != nil {
			return Assignment{}, err
		}
	}
	if exists {
		data, err := d.run(ctx, "inspect", name)
		if err != nil {
			return Assignment{}, err
		}
		var states []dockerState
		if json.Unmarshal(data, &states) != nil || len(states) != 1 || states[0].Config.Labels["wia.user"] != user || states[0].Config.Labels["wia.deployment"] != deployment || states[0].Config.Labels["wia.generation"] != generation || states[0].Config.Image != d.Config.WorkerImage {
			return Assignment{}, ErrWorkerOwnership
		}
		if !states[0].State.Running {
			if _, err = d.run(ctx, "start", name); err != nil {
				return Assignment{}, err
			}
		}
	} else {
		generation, err = d.Accounts.ReplaceGeneration(ctx, user)
		if err != nil {
			return Assignment{}, err
		}
		a.Generation = generation
		a.Token = d.Accounts.Digest("worker:" + user + ":" + generation)
		args := []string{"create", "--name", name, "--label", "wia.managed=1", "--label", "wia.deployment=" + deployment, "--label", "wia.user=" + user, "--label", "wia.generation=" + generation,
			"--network", d.Config.WorkerNetwork, "--memory", strconv.Itoa(d.Config.WorkerMemoryMB) + "m", "--cpus", d.Config.WorkerCPUs, "--pids-limit", "128", "--read-only", "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--restart", "no",
			"--mount", "type=volume,source=" + name + "-data,target=/data", "--env", "WIA_GAME_ID=" + d.Config.GameID, "--env", "WIA_WORKER_ROOT=/data", "--env", "WIA_WORKER_TOKEN=" + a.Token,
			"--env", "WIA_MODEL_URL=" + d.Config.ModelPrivateURL, "--env", "WIA_MODEL=" + d.Config.Model, "--env", "WIA_MODEL_CONTEXT_TOKENS=" + strconv.Itoa(d.Config.ContextTokens), "--env", "WIA_MODEL_OUTPUT_TOKENS=" + strconv.Itoa(d.Config.OutputTokens), d.Config.WorkerImage}
		if _, err = d.run(ctx, args...); err != nil {
			return Assignment{}, err
		}
		if _, err = d.run(ctx, "start", name); err != nil {
			return Assignment{}, err
		}
	}
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if d.probe(ctx, a) == nil {
			d.allocations[user] = a
			return a, nil
		}
		select {
		case <-ctx.Done():
			return Assignment{}, ctx.Err()
		case <-deadline.C:
			return Assignment{}, errors.New("worker_start_failed")
		case <-ticker.C:
		}
	}
}

func (d *DockerWorkers) Authenticate(ctx context.Context, token string) (Assignment, error) {
	items, err := d.Accounts.Generations(ctx)
	if err != nil {
		return Assignment{}, err
	}
	for user, generation := range items {
		expected := d.Accounts.Digest("worker:" + user + ":" + generation)
		if subtleToken(token, expected) {
			return Assignment{UserID: user, Generation: generation, Token: expected, GRPC: "wia-" + user + ":50051", API: "http://wia-" + user + ":8080"}, nil
		}
	}
	return Assignment{}, accounts.ErrCredentials
}

func (d *DockerWorkers) Close(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	names, err := d.run(ctx, "ps", "--filter", "label=wia.deployment="+DeploymentID(d.Accounts), "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	var failures []error
	for _, name := range strings.Fields(string(names)) {
		if !strings.HasPrefix(name, "wia-usr_") || !safeID.MatchString(strings.TrimPrefix(name, "wia-")) {
			return errors.New("unexpected managed container")
		}
		if _, err = d.run(ctx, "stop", "--time", "30", name); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
