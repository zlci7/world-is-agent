package gateway

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	protocol "gameagent/protocol/gen/go/gameagent/protocol/v1alpha2"
	"gameagent/runtime/internal/task"
	"modernc.org/sqlite"
)

var cleanupFaultID atomic.Int64

func cleanupSQLFault(t *testing.T, failures int64) (string, *atomic.Int64) {
	t.Helper()
	name := fmt.Sprintf("task_cleanup_fault_%d", cleanupFaultID.Add(1))
	calls := &atomic.Int64{}
	if err := sqlite.RegisterScalarFunction(name, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if calls.Add(1) <= failures {
			return nil, errors.New("transient task storage failure")
		}
		return args[0], nil
	}); err != nil {
		t.Fatal(err)
	}
	return name, calls
}

func TestTaskControlPlayerCancellationPersistenceRetry(t *testing.T) {
	for _, exhaust := range []bool{false, true} {
		t.Run(fmt.Sprint(exhaust), func(t *testing.T) {
			failures := int64(1)
			if exhaust {
				failures = 100
			}
			function, calls := cleanupSQLFault(t, failures)
			f := newTaskWireFixture(t, false)
			r, op := seedWireOperation(t, f)
			db, err := sql.Open("sqlite", f.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TRIGGER cancel_cleanup_fault BEFORE UPDATE ON tasks WHEN json_array_length(NEW.record_json,'$.cleanup') > 0 BEGIN SELECT ` + function + `(NEW.record_json); END`); err != nil {
				t.Fatal(err)
			}
			f.model.mode, f.model.taskID = "player_cancel", r.ID
			f.event("player-cancel", nil, &protocol.InteractionSource{SourceId: "cancel-input", Kind: "player", PlayerEntityId: "player", Scope: taskScopeToProtocol(f.head.Binding)})
			if f.next().GetEventAck() == nil {
				t.Fatal("no cancel ack")
			}
			f.observe(f.next())
			control := f.next().GetTaskControl()
			if control == nil {
				t.Fatal("no cancel release")
			}
			f.send(&protocol.AdapterMessage{Payload: &protocol.AdapterMessage_TaskControlResult{TaskControlResult: &protocol.TaskControlResult{Scope: control.Scope, TaskId: r.ID, OperationId: op.ID, RequestId: control.RequestId, Status: "released"}}})
			completion := f.next().GetTurnCompletion()
			if completion == nil {
				t.Fatal("no cancellation completion")
			}
			stored, err := f.service.Read(f.ctx, f.key, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.State != task.StateCancelled {
				t.Fatalf("cancel lost: %+v", stored)
			}
			if exhaust {
				if calls.Load() != 3 {
					t.Fatalf("cleanup retry count=%d want 3", calls.Load())
				}
				if _, ready := f.server.worlds.Current(f.head.Binding.World); ready {
					t.Fatal("exhausted cancel cleanup left task admission ready")
				}
			} else if len(stored.Cleanup) != 1 || stored.Cleanup[0].Status != "released" || calls.Load() != 2 {
				t.Fatalf("cancel cleanup was abandoned after first storage failure: cleanup=%+v attempts=%d", stored.Cleanup, calls.Load())
			}
			select {
			case m := <-f.messages:
				t.Fatalf("cancel cleanup resent control: %v", m)
			default:
			}
		})
	}
}

func TestTaskControlPlayerCancellationReadRetry(t *testing.T) {
	for _, exhaust := range []bool{false, true} {
		t.Run(fmt.Sprint(exhaust), func(t *testing.T) {
			failures := int64(1)
			if exhaust {
				failures = 100
			}
			function, calls := cleanupSQLFault(t, failures)
			f := newTaskWireFixture(t, false)
			f.server.dispatcher.Stop()
			source := task.SourceRef{Kind: task.SourceKindInteraction, EventID: "player", TurnID: "turn", CallID: "create"}
			exec := task.ExecutionContext{Owner: f.key, Binding: f.head.Binding, Clock: f.head.Clock, Source: source}
			created, err := f.service.Create(f.ctx, exec, task.TaskSpec{Instruction: "Inspect later", ClockID: "game", WakeAt: 50, DeadlineAt: 100, ResultContract: task.ResultContractAuthoritativeEvidence, Source: source}, task.Admission{})
			if err != nil {
				t.Fatal(err)
			}
			exec.TaskID, exec.ExpectedRevision, exec.Source.CallID = created.Task.ID, created.Task.Revision, "cancel"
			if _, err := f.service.ApplyIntent(f.ctx, exec, task.Intent{Kind: "cancel", Reason: "player request"}); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", f.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rows, err := db.Query("PRAGMA table_info(tasks)")
			if err != nil {
				t.Fatal(err)
			}
			var columns []string
			for rows.Next() {
				var cid, nn, pk int
				var name, typ string
				var dflt any
				if err := rows.Scan(&cid, &name, &typ, &nn, &dflt, &pk); err != nil {
					t.Fatal(err)
				}
				if name == "record_json" {
					columns = append(columns, function+`(record_json) AS record_json`)
				} else {
					columns = append(columns, `"`+name+`"`)
				}
			}
			rows.Close()
			if _, err := db.Exec("ALTER TABLE tasks RENAME TO saved_tasks"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE VIEW tasks AS SELECT " + strings.Join(columns, ",") + " FROM saved_tasks"); err != nil {
				t.Fatal(err)
			}
			defer func() { db.Exec("DROP VIEW tasks"); db.Exec("ALTER TABLE saved_tasks RENAME TO tasks") }()
			entry, _ := f.server.worlds.Current(f.head.Binding.World)
			err = entry.Environment.(*streamEnvironment).ReleaseTask(f.ctx, created.Task)
			if exhaust {
				if err == nil || calls.Load() != 3 {
					t.Fatalf("read exhaustion err=%v calls=%d", err, calls.Load())
				}
				if _, ready := f.server.worlds.Current(f.head.Binding.World); ready {
					t.Fatal("read exhaustion retained task authority")
				}
			} else if err != nil || calls.Load() < 2 {
				t.Fatalf("committed cancellation read was not retried: err=%v calls=%d", err, calls.Load())
			}
			select {
			case m := <-f.messages:
				t.Fatalf("task without operations emitted control: %v", m)
			default:
			}
		})
	}
}
