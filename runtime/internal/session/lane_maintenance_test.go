package session

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"testing/synctest"
)

func TestExecutionLaneMaintenanceYieldsToQueuedPlayers(t *testing.T) {
	for _, size := range []int{1, 3} {
		t.Run(fmt.Sprintf("queueSize%d", size), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lane := newMaintenanceTestLane(t, context.Background(), size)
				release := make(chan struct{})
				mustEnqueue(t, lane, Task{Run: func(ctx context.Context) {
					select {
					case <-release:
					case <-ctx.Done():
					}
				}})
				synctest.Wait()

				events := make(chan string, size+2)
				mustEnqueueMaintenance(t, lane, Task{Run: func(context.Context) { events <- "maintenance" }})
				for i := 0; i < size; i++ {
					mustEnqueue(t, lane, Task{Run: func(context.Context) {
						events <- fmt.Sprintf("player%d", i)
						if i == 0 {
							if err := lane.Enqueue(Task{Run: func(context.Context) { events <- "follow-up" }}); err != nil {
								t.Errorf("enqueue from active player: %v", err)
							}
						}
					}})
				}
				if err := lane.Enqueue(Task{}); !errors.Is(err, ErrLaneFull) {
					t.Fatalf("player overflow = %v, want ErrLaneFull", err)
				}
				close(release)
				synctest.Wait()
				for i := 0; i < size; i++ {
					assertMaintenanceEvent(t, events, fmt.Sprintf("player%d", i))
				}
				assertMaintenanceEvent(t, events, "follow-up")
				assertMaintenanceEvent(t, events, "maintenance")
			})
		})
	}
}

func TestExecutionLaneMaintenanceCoalescesWithoutConsumingPlayerCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lane := newMaintenanceTestLane(t, context.Background(), 1)
		admitted := make(chan struct{})
		mustEnqueue(t, lane, Task{Admitted: admitted})
		synctest.Wait()
		events := make(chan string, 34)
		mustEnqueue(t, lane, Task{Run: func(context.Context) { events <- "player" }})
		for i := 0; i < 32; i++ {
			mustEnqueueMaintenance(t, lane, Task{
				Admitted: make(chan struct{}),
				Run:      func(context.Context) { events <- "superseded:run" },
				Abort:    func(AbortReason) { events <- "superseded:abort" },
			})
		}
		mustEnqueueMaintenance(t, lane, Task{Run: func(context.Context) { events <- "latest" }})
		if err := lane.Enqueue(Task{}); !errors.Is(err, ErrLaneFull) {
			t.Fatalf("player overflow = %v, want ErrLaneFull", err)
		}
		close(admitted)
		synctest.Wait()
		lane.Close()
		<-lane.Done()
		assertMaintenanceEvent(t, events, "player")
		assertMaintenanceEvent(t, events, "latest")
		assertNoMaintenanceEvent(t, events)
	})
}

func TestExecutionLaneMaintenanceWakesIdleLaneAndWaitsForAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lane := newMaintenanceTestLane(t, context.Background(), 1)
		events := make(chan string, 3)
		synctest.Wait()
		admitted := make(chan struct{})
		mustEnqueueMaintenance(t, lane, Task{
			Admitted: admitted,
			Run:      func(context.Context) { events <- "maintenance" },
		})
		synctest.Wait()
		mustEnqueue(t, lane, Task{Run: func(context.Context) { events <- "player" }})
		synctest.Wait()
		assertNoMaintenanceEvent(t, events)
		close(admitted)
		synctest.Wait()
		assertMaintenanceEvent(t, events, "maintenance")
		assertMaintenanceEvent(t, events, "player")
		mustEnqueueMaintenance(t, lane, Task{Run: func(context.Context) { events <- "next" }})
		synctest.Wait()
		assertMaintenanceEvent(t, events, "next")
	})
}

func TestExecutionLaneMaintenanceDoesNotPreemptActiveTask(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lane := newMaintenanceTestLane(t, context.Background(), 1)
		release := make(chan struct{})
		events := make(chan string, 8)
		mustEnqueueMaintenance(t, lane, Task{Run: func(ctx context.Context) {
			events <- "active:start"
			select {
			case <-release:
				events <- "active:end"
			case <-ctx.Done():
				events <- "active:cancelled"
			}
		}})
		synctest.Wait()
		assertMaintenanceEvent(t, events, "active:start")
		mustEnqueueMaintenance(t, lane, Task{
			Run:   func(context.Context) { events <- "superseded:run" },
			Abort: func(AbortReason) { events <- "superseded:abort" },
		})
		mustEnqueue(t, lane, Task{Run: func(context.Context) { events <- "player" }})
		mustEnqueueMaintenance(t, lane, Task{Run: func(context.Context) { events <- "latest" }})
		synctest.Wait()
		assertNoMaintenanceEvent(t, events)
		close(release)
		synctest.Wait()
		assertMaintenanceEvent(t, events, "active:end")
		assertMaintenanceEvent(t, events, "player")
		assertMaintenanceEvent(t, events, "latest")
		assertNoMaintenanceEvent(t, events)
	})
}

func TestExecutionLaneActiveMaintenancePreservesPlayerAdmissionCapacity(t *testing.T) {
	for _, size := range []int{1, 3} {
		for _, waitingAdmission := range []bool{false, true} {
			t.Run(fmt.Sprintf("queueSize%d/waitingAdmission%t", size, waitingAdmission), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					lane := newMaintenanceTestLane(t, context.Background(), size)
					admitted := make(chan struct{})
					releaseMaintenance := make(chan struct{})
					releaseCleanup := make(chan struct{})
					releasePlayer := make(chan struct{})
					defer close(releaseMaintenance)
					defer close(releaseCleanup)
					defer close(releasePlayer)
					if !waitingAdmission {
						close(admitted)
					}
					events := make(chan string, size+4)
					mustEnqueueMaintenance(t, lane, Task{
						Admitted: admitted,
						Run: func(context.Context) {
							defer func() {
								events <- "maintenance:cleanup"
								<-releaseCleanup
								events <- "maintenance:done"
							}()
							events <- "maintenance:start"
							<-releaseMaintenance
						},
					})
					synctest.Wait()
					if !waitingAdmission {
						assertMaintenanceEvent(t, events, "maintenance:start")
					}
					for i := 0; i <= size; i++ {
						mustEnqueue(t, lane, Task{
							ID: fmt.Sprintf("player%d", i+1),
							Run: func(context.Context) {
								events <- fmt.Sprintf("player%d", i+1)
								if i == 0 {
									<-releasePlayer
								}
							},
						})
					}
					if err := lane.Enqueue(Task{}); !errors.Is(err, ErrLaneFull) {
						t.Fatalf("overflow during maintenance = %v, want ErrLaneFull", err)
					}
					synctest.Wait()
					assertNoMaintenanceEvent(t, events)
					if waitingAdmission {
						close(admitted)
						synctest.Wait()
						assertMaintenanceEvent(t, events, "maintenance:start")
						assertNoMaintenanceEvent(t, events)
					}
					releaseMaintenance <- struct{}{}
					synctest.Wait()
					assertMaintenanceEvent(t, events, "maintenance:cleanup")
					assertNoMaintenanceEvent(t, events)
					releaseCleanup <- struct{}{}
					synctest.Wait()
					assertMaintenanceEvent(t, events, "maintenance:done")
					assertMaintenanceEvent(t, events, "player1")
					assertNoMaintenanceEvent(t, events)
					if err := lane.Enqueue(Task{}); !errors.Is(err, ErrLaneFull) {
						t.Fatalf("overflow with active player and %d queued = %v, want ErrLaneFull", size, err)
					}
					releasePlayer <- struct{}{}
					synctest.Wait()
					for i := 1; i <= size; i++ {
						assertMaintenanceEvent(t, events, fmt.Sprintf("player%d", i+1))
					}
					assertNoMaintenanceEvent(t, events)
				})
			})
		}
	}
}

func TestExecutionLaneActiveMaintenanceBoundsConcurrentPlayerAdmission(t *testing.T) {
	for _, size := range []int{1, 3} {
		t.Run(fmt.Sprintf("queueSize%d", size), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lane := newMaintenanceTestLane(t, context.Background(), size)
				mustEnqueueMaintenance(t, lane, Task{Run: func(ctx context.Context) { <-ctx.Done() }})
				synctest.Wait()
				const players = 64
				start := make(chan struct{})
				results := make(chan error, players)
				aborted := make(chan AbortReason, players)
				var wg sync.WaitGroup
				for i := 0; i < players; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						results <- lane.Enqueue(Task{
							Run:   func(context.Context) { t.Error("player ran while maintenance held the lane") },
							Abort: func(reason AbortReason) { aborted <- reason },
						})
					}()
				}
				close(start)
				wg.Wait()
				close(results)
				accepted := 0
				for err := range results {
					if err == nil {
						accepted++
					} else if !errors.Is(err, ErrLaneFull) {
						t.Fatalf("concurrent player admission = %v, want nil or ErrLaneFull", err)
					}
				}
				if accepted != size+1 {
					t.Fatalf("accepted players during maintenance = %d, want %d", accepted, size+1)
				}
				lane.Close()
				<-lane.Done()
				if len(aborted) != accepted {
					t.Fatalf("aborted players = %d, want %d", len(aborted), accepted)
				}
				close(aborted)
				for reason := range aborted {
					if reason != AbortReasonConnectionClosed {
						t.Fatalf("abort reason = %q, want connection_closed", reason)
					}
				}
			})
		})
	}
}

func TestExecutionLaneMaintenanceCancellation(t *testing.T) {
	for _, parentCancel := range []bool{false, true} {
		for _, waitingAdmission := range []bool{false, true} {
			t.Run(fmt.Sprintf("parentCancel%t/waitingAdmission%t", parentCancel, waitingAdmission), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					lane := newMaintenanceTestLane(t, ctx, 1)
					events := make(chan string, 6)
					if !waitingAdmission {
						mustEnqueue(t, lane, Task{Run: func(ctx context.Context) { <-ctx.Done() }})
						synctest.Wait()
						mustEnqueueMaintenance(t, lane, Task{
							Run:   func(context.Context) { events <- "superseded:run" },
							Abort: func(AbortReason) { events <- "superseded:abort" },
						})
					}
					mustEnqueueMaintenance(t, lane, Task{
						Admitted: make(chan struct{}),
						Run:      func(context.Context) { events <- "maintenance:run" },
						Abort: func(reason AbortReason) {
							events <- "maintenance:" + string(reason)
							if err := lane.EnqueueMaintenance(Task{}); !errors.Is(err, ErrLaneClosed) {
								t.Errorf("maintenance enqueue from abort = %v, want ErrLaneClosed", err)
							}
						},
					})
					synctest.Wait()
					mustEnqueue(t, lane, Task{
						Run:   func(context.Context) { events <- "player:run" },
						Abort: func(reason AbortReason) { events <- "player:" + string(reason) },
					})
					if parentCancel {
						cancel()
					} else {
						lane.Close()
					}
					<-lane.Done()
					if waitingAdmission {
						assertMaintenanceEvent(t, events, "maintenance:connection_closed")
						assertMaintenanceEvent(t, events, "player:connection_closed")
					} else {
						assertMaintenanceEvent(t, events, "player:connection_closed")
						assertMaintenanceEvent(t, events, "maintenance:connection_closed")
					}
					if err := lane.EnqueueMaintenance(Task{}); !errors.Is(err, ErrLaneClosed) {
						t.Fatalf("maintenance enqueue after cancellation = %v, want ErrLaneClosed", err)
					}
					assertNoMaintenanceEvent(t, events)
				})
			})
		}
	}
}

func TestExecutionLaneMaintenanceRejectsAdmissionDuringCancellationCleanup(t *testing.T) {
	for _, parentCancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("parentCancel%t", parentCancel), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				lane := newMaintenanceTestLane(t, ctx, 1)
				releaseCleanup := make(chan struct{})
				defer close(releaseCleanup)
				mustEnqueueMaintenance(t, lane, Task{
					Run: func(ctx context.Context) {
						<-ctx.Done()
						<-releaseCleanup
					},
					Abort: func(AbortReason) { t.Error("active maintenance was aborted") },
				})
				synctest.Wait()
				if parentCancel {
					cancel()
				} else {
					lane.Close()
				}
				synctest.Wait()
				assertMaintenanceStillOpen(t, lane.Done())
				if err := lane.Enqueue(Task{}); !errors.Is(err, ErrLaneClosed) {
					t.Fatalf("player enqueue during cleanup = %v, want ErrLaneClosed", err)
				}
				if err := lane.EnqueueMaintenance(Task{}); !errors.Is(err, ErrLaneClosed) {
					t.Fatalf("maintenance enqueue during cleanup = %v, want ErrLaneClosed", err)
				}
				releaseCleanup <- struct{}{}
				<-lane.Done()
			})
		})
	}
}

func TestLaneStoreCloseAndWaitWaitsForMaintenanceCleanupAndAborts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, err := NewLaneStore(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(store.CloseAndWait)
		releaseCleanup := make(chan struct{})
		releaseAbort := make(chan struct{})
		defer close(releaseCleanup)
		defer close(releaseAbort)
		events := make(chan string, 8)
		for _, entity := range []string{"entity:a", "entity:b"} {
			lane, err := store.GetOrCreate(mustResolve(t, "game", "world", entity))
			if err != nil {
				t.Fatal(err)
			}
			mustEnqueueMaintenance(t, lane, Task{
				Run: func(ctx context.Context) {
					<-ctx.Done()
					events <- "cancelled"
					<-releaseCleanup
					events <- "cleaned"
				},
				Abort: func(AbortReason) { t.Error("active maintenance was aborted") },
			})
			synctest.Wait()
			mustEnqueueMaintenance(t, lane, Task{
				Run: func(context.Context) { t.Error("pending maintenance ran during shutdown") },
				Abort: func(reason AbortReason) {
					if reason != AbortReasonConnectionClosed {
						t.Errorf("abort reason = %q", reason)
					}
					events <- "aborting"
					<-releaseAbort
				},
			})
		}
		done := make(chan struct{})
		go func() { store.CloseAndWait(); close(done) }()
		synctest.Wait()
		assertMaintenanceEvent(t, events, "cancelled")
		assertMaintenanceEvent(t, events, "cancelled")
		assertMaintenanceStillOpen(t, done)
		releaseCleanup <- struct{}{}
		releaseCleanup <- struct{}{}
		synctest.Wait()
		counts := make(map[string]int)
		for i := 0; i < 4; i++ {
			counts[receiveString(t, events)]++
		}
		if counts["cleaned"] != 2 || counts["aborting"] != 2 {
			t.Fatalf("cleanup events = %v", counts)
		}
		assertMaintenanceStillOpen(t, done)
		releaseAbort <- struct{}{}
		releaseAbort <- struct{}{}
		<-done
		store.CloseAndWait()
	})
}

func TestExecutionLaneMaintenanceSelectionUsesAdmissionMutex(t *testing.T) {
	lane := newMaintenanceTestLane(t, context.Background(), 1)
	release := make(chan struct{})
	started := make(chan struct{})
	returning := make(chan struct{})
	mustEnqueue(t, lane, Task{Run: func(ctx context.Context) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		close(returning)
	}})
	<-started
	events := make(chan string, 2)
	mustEnqueueMaintenance(t, lane, Task{Run: func(context.Context) { events <- "maintenance" }})
	func() {
		lane.mu.Lock()
		defer lane.mu.Unlock()
		close(release)
		<-returning
		for i := 0; i < 100; i++ {
			runtime.Gosched()
		}
		// Complete a player admission while selection is excluded by the same mutex.
		lane.queue <- Task{Run: func(context.Context) { events <- "player" }}
	}()
	assertMaintenanceEvent(t, events, "player")
	assertMaintenanceEvent(t, events, "maintenance")
}

func TestExecutionLaneMaintenanceConcurrentEnqueueAndClose(t *testing.T) {
	const producers = 32
	for trial := 0; trial < 64; trial++ {
		synctest.Test(t, func(t *testing.T) {
			lane := newMaintenanceTestLane(t, context.Background(), 1)
			mustEnqueue(t, lane, Task{Run: func(ctx context.Context) { <-ctx.Done() }})
			synctest.Wait()
			type result struct {
				id          int
				maintenance bool
				err         error
			}
			results := make(chan result, producers*2)
			aborted := make(chan int, producers*2)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < producers*2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					task := Task{
						Run: func(context.Context) { t.Error("queued task ran during close") },
						Abort: func(reason AbortReason) {
							if reason != AbortReasonConnectionClosed {
								t.Errorf("abort reason = %q", reason)
							}
							aborted <- i
						},
					}
					maintenance := i%2 == 0
					var err error
					if maintenance {
						err = lane.EnqueueMaintenance(task)
					} else {
						err = lane.Enqueue(task)
					}
					results <- result{id: i, maintenance: maintenance, err: err}
				}()
			}
			wg.Add(1)
			go func() { defer wg.Done(); <-start; lane.Close() }()
			close(start)
			wg.Wait()
			<-lane.Done()
			close(results)
			close(aborted)
			accepted := make(map[int]bool)
			maintenanceAccepted, playersAccepted := 0, 0
			for result := range results {
				if result.err == nil {
					accepted[result.id] = true
					if result.maintenance {
						maintenanceAccepted++
					} else {
						playersAccepted++
					}
				} else if !errors.Is(result.err, ErrLaneClosed) && (result.maintenance || !errors.Is(result.err, ErrLaneFull)) {
					t.Fatalf("trial %d enqueue result = %+v", trial, result)
				}
			}
			maintenanceAborted, playersAborted := 0, 0
			for id := range aborted {
				if !accepted[id] {
					t.Fatalf("trial %d: task %d aborted twice or without admission", trial, id)
				}
				delete(accepted, id)
				if id%2 == 0 {
					maintenanceAborted++
				} else {
					playersAborted++
				}
			}
			wantMaintenanceAborted := 0
			if maintenanceAccepted > 0 {
				wantMaintenanceAborted = 1
			}
			if maintenanceAborted != wantMaintenanceAborted || playersAborted != playersAccepted {
				t.Fatalf("trial %d: aborted maintenance/player = %d/%d, want %d/%d", trial,
					maintenanceAborted, playersAborted, wantMaintenanceAborted, playersAccepted)
			}
		})
	}
}

func newMaintenanceTestLane(t *testing.T, ctx context.Context, size int) *ExecutionLane {
	t.Helper()
	lane, err := NewExecutionLane(ctx, size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lane.Close(); <-lane.Done() })
	return lane
}

func mustEnqueueMaintenance(t *testing.T, lane *ExecutionLane, task Task) {
	t.Helper()
	if err := lane.EnqueueMaintenance(task); err != nil {
		t.Fatalf("EnqueueMaintenance(%q) returned error: %v", task.ID, err)
	}
}

func assertMaintenanceEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	if got := receiveString(t, events); got != want {
		t.Fatalf("event = %q, want %q", got, want)
	}
}

func assertNoMaintenanceEvent(t *testing.T, events <-chan string) {
	t.Helper()
	select {
	case got := <-events:
		t.Fatalf("unexpected event: %q", got)
	default:
	}
}

func assertMaintenanceStillOpen(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("CloseAndWait returned before cleanup finished")
	default:
	}
}
