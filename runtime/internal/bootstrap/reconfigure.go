package bootstrap

import (
	"errors"
	"path/filepath"

	"gameagent/runtime/internal/agent"
	"gameagent/runtime/internal/definition"
	"gameagent/runtime/internal/gateway"
	"gameagent/runtime/internal/model"
)

func (r *Runtime) loadGame(id string) (agent.Config, definition.Catalog, string, error) {
	path := r.layout.ConfigPath(filepath.Join("games", id, agentConfigFile))
	if r.overridePath != "" {
		path = r.overridePath
	}
	cfg, err := strictAgentConfig(path)
	if err != nil {
		return cfg, definition.Catalog{}, path, err
	}
	cfg = resolveConfigPaths(r.layout.Root(), cfg)
	if err := r.validateOverrideRoot(cfg); err != nil {
		return cfg, definition.Catalog{}, path, err
	}
	catalog, err := definition.LoadGameCatalogFromDir(cfg.DefinitionCatalogRoot, id)
	return cfg, catalog, path, err
}

// replaceLocked holds setupMu. Disk still refers to the previous generation
// until the replacement owns its resources and is ready for publication.
func (r *Runtime) replaceLocked(cfg agent.Config, catalog definition.Catalog, provider model.Provider, next Snapshot, commit func() error) error {
	r.mu.Lock()
	old, previous, streams := r.bundle, r.snapshot, r.streams
	r.snapshot.State, r.snapshot.Ready = StateReconfiguring, false
	r.snapshot.Reason, r.snapshot.ReasonCode = "Applying Runtime settings", "reconfiguring"
	r.bundle = nil
	streams.Stop()
	r.mu.Unlock()
	streams.Close()
	if old != nil {
		if err := closeBundle(old); err != nil {
			return r.publishFailure(previous, StateBlocked, "initialization_failed", err)
		}
	}
	candidate, err := r.prepareBundle(cfg, catalog, provider)
	if err == nil && r.testBeforePublish != nil {
		if err = r.testBeforePublish(); err != nil {
			err = r.abortCandidate(candidate, err)
		}
	}
	if err == nil {
		if err = commit(); err != nil {
			err = r.abortCandidate(candidate, err)
		}
	}
	if err != nil {
		var failure *initializationError
		if errors.As(err, &failure) && failure.unsafe {
			return r.publishFailure(previous, StateBlocked, "initialization_failed", err)
		}
		if old == nil {
			// Recoverable setup keeps an open admission owner for later initialization.
			// The retired group has already drained all pre-Hello handlers.
			r.mu.Lock()
			r.streams = &gateway.StreamGroup{}
			r.mu.Unlock()
			return r.publishFailure(previous, StateNeedsConfiguration, "initialization_failed", err)
		}
		restored, restoreErr := r.prepareBundle(old.config, old.catalog, old.provider)
		if restoreErr != nil {
			return r.publishFailure(previous, StateBlocked, "initialization_failed", errors.Join(err, restoreErr))
		}
		r.mu.Lock()
		r.bundle, r.snapshot, r.streams = restored, previous, &gateway.StreamGroup{}
		r.mu.Unlock()
		return err
	}
	next.LoadedGame = cloneGame(next.ConfiguredGame)
	next.State, next.Ready = StateReady, true
	r.mu.Lock()
	r.bundle, r.snapshot, r.streams = candidate, next, &gateway.StreamGroup{}
	r.lastConnectionError = nil
	r.mu.Unlock()
	return nil
}
