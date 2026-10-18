package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"gameagent/runtime/internal/accounts"
)

type dockerFixture struct {
	volume, container, running          bool
	generation, user, image, deployment string
	created, started                    int
	failInventory                       bool
	commands                            [][]string
}

func (f *dockerFixture) run(_ context.Context, args ...string) ([]byte, error) {
	f.commands = append(f.commands, append([]string(nil), args...))
	switch args[0] {
	case "ps":
		if f.failInventory {
			return nil, errors.New("inventory failed")
		}
		if f.container {
			return []byte("wia-" + f.user), nil
		}
		return nil, nil
	case "volume":
		if args[1] == "inspect" {
			return json.Marshal([]map[string]any{{"Labels": map[string]string{"wia.user": f.user, "wia.deployment": f.deployment}}})
		}
		if args[1] == "ls" {
			if f.volume {
				return []byte("wia-" + f.user + "-data"), nil
			}
			return nil, nil
		}
		if args[1] == "create" {
			f.volume = true
			for _, arg := range args {
				if strings.HasPrefix(arg, "wia.deployment=") {
					f.deployment = strings.TrimPrefix(arg, "wia.deployment=")
				}
			}
			return []byte("created"), nil
		}
	case "inspect":
		state := dockerState{}
		state.State.Running = f.running
		state.Config.Image = f.image
		state.Config.Labels = map[string]string{"wia.user": f.user, "wia.generation": f.generation, "wia.deployment": f.deployment}
		return json.Marshal([]dockerState{state})
	case "create":
		f.created++
		f.container = true
		f.image = args[len(args)-1]
		for _, arg := range args {
			if strings.HasPrefix(arg, "wia.generation=") {
				f.generation = strings.TrimPrefix(arg, "wia.generation=")
			}
		}
		return []byte("container-id"), nil
	case "start":
		f.started++
		f.running = true
		return []byte("started"), nil
	case "stop":
		f.running = false
		return []byte("stopped"), nil
	}
	return nil, errors.New("unexpected docker command")
}

func TestWorkerAllocationUsesOneWriterAndRetainsVolume(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	user := f.user(t, "worker@example.com")
	c := f.s.Config
	c.MaxWorkers = 3
	c.WorkerImage = "wia-worker:test"
	c.WorkerMemoryMB = 512
	c.WorkerCPUs = "1.0"
	c.WorkerNetwork = "wia-private"
	engine := &dockerFixture{user: user.ID}
	manager := NewDockerWorkers(c, f.store)
	manager.run = engine.run
	manager.probe = func(context.Context, Assignment) error { return nil }
	var wg sync.WaitGroup
	results := make(chan Assignment, 10)
	failures := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := manager.Ensure(ctx, user.ID)
			if err != nil {
				failures <- err
			} else {
				results <- a
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	first := ""
	for a := range results {
		if first == "" {
			first = a.Token
		}
		if a.Token != first {
			t.Fatal("concurrent allocations used different credentials")
		}
	}
	if engine.created != 1 || engine.started != 1 {
		t.Fatalf("writers created=%d started=%d", engine.created, engine.started)
	}
	restarted := NewDockerWorkers(c, f.store)
	restarted.run = engine.run
	restarted.probe = manager.probe
	if _, err := restarted.Ensure(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if engine.created != 1 {
		t.Fatal("controller restart created another writer")
	}
	engine.running = false
	restarted.allocations = map[string]Assignment{}
	if _, err := restarted.Ensure(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if engine.created != 1 || engine.started != 2 {
		t.Fatal("worker restart replaced account volume")
	}
	engine.container = false
	restarted.allocations = map[string]Assignment{}
	newAssignment, err := restarted.Ensure(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newAssignment.Token == first {
		t.Fatal("recreated allocation retained retired service credential")
	}
	if _, err = restarted.Authenticate(ctx, first); !errors.Is(err, accounts.ErrCredentials) {
		t.Fatal("retired worker credential admitted")
	}
	if owner, err := restarted.Authenticate(ctx, newAssignment.Token); err != nil || owner.UserID != user.ID {
		t.Fatal("new service credential rejected")
	}
}

func TestMissingAssignedVolumeAndInventoryFailuresDoNotCreateEmptyWorkers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "missing-volume@example.com")
	if err := f.store.MarkVolumeInitialized(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	c := f.s.Config
	c.MaxWorkers = 3
	engine := &dockerFixture{user: u.ID}
	manager := NewDockerWorkers(c, f.store)
	manager.run = engine.run
	manager.probe = func(context.Context, Assignment) error { return nil }
	if _, err := manager.Ensure(ctx, u.ID); err == nil || !strings.Contains(err.Error(), "worker_data_missing") {
		t.Fatalf("missing data: %v", err)
	}
	if engine.created != 0 || engine.volume {
		t.Fatal("missing data was replaced")
	}
	engine.failInventory = true
	if _, err := manager.Ensure(ctx, u.ID); err == nil {
		t.Fatal("inventory failure treated as empty")
	}
	if engine.created != 0 {
		t.Fatal("uncertain inventory created a writer")
	}
	if _, err := manager.Ensure(ctx, "../../other-account"); err == nil {
		t.Fatal("unsafe account path admitted")
	}
}

func TestWorkerOwnershipMismatchDoesNotStartOrAdoptContainer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "ownership@example.com")
	c := f.s.Config
	c.WorkerImage = "wia-worker:expected"
	c.MaxWorkers = 3
	engine := &dockerFixture{user: u.ID, container: true, volume: true, generation: "foreign-generation", image: c.WorkerImage, deployment: DeploymentID(f.store)}
	manager := NewDockerWorkers(c, f.store)
	manager.run = engine.run
	manager.probe = func(context.Context, Assignment) error { return nil }
	if _, err := manager.Ensure(ctx, u.ID); err == nil || !strings.Contains(err.Error(), "worker_ownership_conflict") {
		t.Fatalf("ownership mismatch: %v", err)
	}
	if engine.started != 0 || engine.created != 0 {
		t.Fatal("foreign allocation changed")
	}
}

func TestWorkerRejectsForeignVolumeAndShutdownScopesItsDeployment(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t, "foreign-volume@example.com")
	engine := &dockerFixture{user: u.ID, volume: true, deployment: "another-deployment"}
	c := f.s.Config
	c.MaxWorkers = 3
	manager := NewDockerWorkers(c, f.store)
	manager.run = engine.run
	if _, err := manager.Ensure(ctx, u.ID); err == nil || !strings.Contains(err.Error(), "worker_volume_ownership_conflict") {
		t.Fatalf("foreign volume: %v", err)
	}
	if engine.created != 0 || engine.started != 0 {
		t.Fatal("foreign volume was mounted")
	}
	owned := "wia-" + u.ID
	foreign := "wia-usr_00000000000000000000000000000000"
	running := map[string]bool{owned: true, foreign: true}
	manager.run = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ps" {
			if strings.Contains(strings.Join(args, " "), "label=wia.deployment="+DeploymentID(f.store)) {
				return []byte(owned), nil
			}
			return []byte(owned + "\n" + foreign), nil
		}
		if args[0] == "stop" {
			running[args[len(args)-1]] = false
			return nil, nil
		}
		return nil, errors.New("unexpected command")
	}
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if running[owned] || !running[foreign] {
		t.Fatal("shutdown crossed deployment ownership")
	}
}
