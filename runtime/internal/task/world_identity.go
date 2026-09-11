package task

import (
	"context"
	"database/sql"
	"errors"

	"gameagent/runtime/internal/session"
)

var errAmbiguousWorldTaskIdentityGraph = errors.New("ambiguous world task identity graph")

type worldOperationIdentity struct {
	operation Operation
	owner     session.AgentSessionKey
	taskID    string
	clockID   string
}

type worldEvidenceIdentity struct {
	evidence Evidence
	owner    session.AgentSessionKey
	taskID   string
}

type worldTaskIdentity struct {
	owner  session.AgentSessionKey
	taskID string
}

type worldTaskIdentityGraph struct {
	tasks      map[worldTaskIdentity]storedIntentTask
	operations map[string]worldOperationIdentity
	facts      map[string]worldEvidenceIdentity
	sources    map[evidenceSourceIdentity]struct{}
}

type worldTaskQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadWorldTaskIdentityGraph(ctx context.Context, queryer worldTaskQueryer, world WorldKey) (worldTaskIdentityGraph, error) {
	if ctx == nil || queryer == nil {
		return worldTaskIdentityGraph{}, ErrInvalidTaskSpec
	}
	if err := world.Validate(); err != nil {
		return worldTaskIdentityGraph{}, err
	}
	rows, err := queryer.QueryContext(ctx, taskSelectSQL+` WHERE game_id = ? AND world_id = ? ORDER BY entity_id, task_id`,
		world.GameID, world.WorldID)
	if err != nil {
		return worldTaskIdentityGraph{}, err
	}
	defer rows.Close()

	graph := worldTaskIdentityGraph{
		tasks:      make(map[worldTaskIdentity]storedIntentTask),
		operations: make(map[string]worldOperationIdentity),
		facts:      make(map[string]worldEvidenceIdentity),
		sources:    make(map[evidenceSourceIdentity]struct{}),
	}
	for rows.Next() {
		record, _, _, history, err := scanTaskRowWithMetadata(rows)
		if err != nil {
			return worldTaskIdentityGraph{}, err
		}
		identity := worldTaskIdentity{owner: record.Owner, taskID: record.ID}
		if _, duplicate := graph.tasks[identity]; duplicate {
			return worldTaskIdentityGraph{}, errAmbiguousWorldTaskIdentityGraph
		}
		graph.tasks[identity] = storedIntentTask{record: record, history: history}
		for _, operation := range record.Operations {
			if _, duplicate := graph.operations[operation.ID]; duplicate {
				return worldTaskIdentityGraph{}, errAmbiguousWorldTaskIdentityGraph
			}
			graph.operations[operation.ID] = worldOperationIdentity{
				operation: operation,
				owner:     record.Owner,
				taskID:    record.ID,
				clockID:   record.Spec.ClockID,
			}
		}
		for _, evidence := range record.Evidence {
			identity := worldEvidenceIdentity{evidence: evidence, owner: record.Owner, taskID: record.ID}
			if _, duplicate := graph.facts[evidence.FactID]; duplicate {
				return worldTaskIdentityGraph{}, errAmbiguousWorldTaskIdentityGraph
			}
			graph.facts[evidence.FactID] = identity
			if source, complete := completeEvidenceSourceIdentity(evidence.Source); complete {
				if _, duplicate := graph.sources[source]; duplicate {
					return worldTaskIdentityGraph{}, errAmbiguousWorldTaskIdentityGraph
				}
				graph.sources[source] = struct{}{}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return worldTaskIdentityGraph{}, err
	}
	return graph, nil
}

func (s *SQLiteStore) loadWorldTaskIdentityGraphTx(ctx context.Context, tx *sql.Tx, world WorldKey) (worldTaskIdentityGraph, error) {
	return loadWorldTaskIdentityGraph(ctx, tx, world)
}

func (s *SQLiteStore) validateWorldTaskIdentityGraph(ctx context.Context, world WorldKey) error {
	_, err := loadWorldTaskIdentityGraph(ctx, s.db, world)
	if errors.Is(err, errAmbiguousWorldTaskIdentityGraph) {
		return ErrInvalidTaskSpec
	}
	return classifyStoreError(err)
}
