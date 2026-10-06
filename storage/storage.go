package storage

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/psviderski/uncloud/api/pb"
	"github.com/psviderski/uncloud/pkg/api"
	"github.com/psviderski/uncloud/pkg/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const storeReplicationTimeout = 10 * time.Second

// Store writes a value to the cluster store.
func (s *Storage) Store(ctx context.Context, key string, value []byte) error {
	if _, err := s.client.Caddy.Storage.Store(ctx, &pb.StoreCaddyStorageRequest{Key: key, Value: value}); err != nil {
		return storageError("store", key, err)
	}
	return nil
}

// Load reads a value from the cluster store.
func (s *Storage) Load(ctx context.Context, key string) ([]byte, error) {
	_ = s.waitForStoreReplication(ctx, s.log.With("operation", "load", "key", key))

	resp, err := s.client.Caddy.Storage.Load(ctx, &pb.LoadCaddyStorageRequest{Key: key})
	if err != nil {
		return nil, storageError("load", key, err)
	}
	return resp.Value, nil
}

// Delete removes a key and its descendants from the cluster store.
func (s *Storage) Delete(ctx context.Context, key string) error {
	if _, err := s.client.Caddy.Storage.Delete(ctx, &pb.DeleteCaddyStorageRequest{Key: key}); err != nil {
		return storageError("delete", key, err)
	}
	return nil
}

// Exists reports whether a key exists in the cluster store.
func (s *Storage) Exists(ctx context.Context, key string) bool {
	_, err := s.Stat(ctx, key)
	return err == nil
}

// List returns keys under prefix from the cluster store.
func (s *Storage) List(ctx context.Context, prefix string, recursive bool) ([]string, error) {
	_ = s.waitForStoreReplication(ctx, s.log.With("operation", "list", "prefix", prefix))

	resp, err := s.client.Caddy.Storage.List(ctx, &pb.ListCaddyStorageRequest{
		Prefix:    prefix,
		Recursive: recursive,
	})
	if err != nil {
		return nil, storageError("list", prefix, err)
	}
	return resp.Keys, nil
}

// Stat returns information about a key in the cluster store.
func (s *Storage) Stat(ctx context.Context, key string) (certmagic.KeyInfo, error) {
	_ = s.waitForStoreReplication(ctx, s.log.With("operation", "stat", "key", key))

	resp, err := s.client.Caddy.Storage.Stat(ctx, &pb.StatCaddyStorageRequest{Key: key})
	if err != nil {
		return certmagic.KeyInfo{}, storageError("stat", key, err)
	}

	info := certmagic.KeyInfo{
		Key:        resp.Key,
		Size:       resp.Size,
		IsTerminal: resp.IsTerminal,
	}
	if resp.UpdatedAt != nil {
		if err := resp.UpdatedAt.CheckValid(); err != nil {
			return certmagic.KeyInfo{}, storageError("stat", key, fmt.Errorf("invalid updated_at timestamp: %w", err))
		}
		info.Modified = resp.UpdatedAt.AsTime()
	}
	return info, nil
}

// waitForStoreReplication collects store versions observed on responding machines and makes a best-effort wait for
// local replication. Times out after storeReplicationTimeout.
// The wait does not guarantee an exact snapshot or cover writes on unavailable machines.
func (s *Storage) waitForStoreReplication(ctx context.Context, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, storeReplicationTimeout)
	defer cancel()
	// Stop waiting when Caddy unloads the module (cancels s.ctx), even if the caller's ctx is still active.
	stopOnCleanup := context.AfterFunc(s.ctx, cancel)
	defer stopOnCleanup()

	version, machines, err := s.clusterStoreVersion(ctx, log)
	if err != nil {
		log.Warn("failed to inspect cluster store version", "error", err)
		return err
	}

	started := time.Now()
	log.Debug("waiting for local store replication", "machine_names", machines, "store_version", version)
	if err = s.client.WaitForStoreVersion(ctx, version); err != nil {
		log.Warn("failed to wait for local store replication", "duration", time.Since(started), "error", err)
		return err
	}
	log.Debug("local store replication complete", "duration", time.Since(started))

	return nil
}

// clusterStoreVersion returns the per-actor maximum store versions from responding machines and their names.
func (s *Storage) clusterStoreVersion(ctx context.Context, log *slog.Logger) (api.StoreVersion, []string, error) {
	resp, err := s.client.MachineClient.InspectMachine(client.ProxyMachinesContext(ctx, nil), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect machines for store versions: %w", err)
	}

	maxVersion := make(api.StoreVersion)
	machines := make([]string, 0, len(resp.Machines))
	for _, m := range resp.Machines {
		if m.Metadata.Error != "" {
			log.Warn("skipping machine when collecting store versions",
				"id", m.Metadata.MachineId, "name", m.Metadata.MachineName, "error", m.Metadata.Error)
			continue
		}
		machines = append(machines, m.Metadata.MachineName)
		maxVersion.MergeMax(m.StoreVersion)
	}
	slices.Sort(machines)

	return maxVersion, machines, nil
}

func storageError(operation, key string, err error) error {
	if status.Code(err) == codes.NotFound {
		err = fs.ErrNotExist
	}
	return fmt.Errorf("uncloud storage: %s key '%s': %w", operation, key, err)
}
