// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package clustermesh

import (
	"context"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/cilium/hive/cell"
	"github.com/cilium/hive/hivetest"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	cmtypes "github.com/cilium/cilium/pkg/clustermesh/types"
	"github.com/cilium/cilium/pkg/hive"
	"github.com/cilium/cilium/pkg/kvstore"
	"github.com/cilium/cilium/pkg/lock"
)

const (
	users1 = "users:\n- name: foo\n  role: r1\n- name: bar\n  role: r2\n- name: qux\n  role: r3\n"
	users2 = "users:\n- name: baz\n  role: r3\n- name: foo\n  role: r1\n- name: qux\n  role: r4\n"
)

type fakeUserMgmtClient struct {
	mu      lock.RWMutex
	created map[string]string
	deleted map[string]int
}

func (f *fakeUserMgmtClient) init() {
	f.created = make(map[string]string)
	f.deleted = make(map[string]int)
}

func (f *fakeUserMgmtClient) UserEnforcePresence(_ context.Context, name string, roles []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	// The existing value (if any) is concatenated, to detect if this is called twice for the same name
	f.created[name] = f.created[name] + strings.Join(roles, "|")
	return nil
}

func (f *fakeUserMgmtClient) UserEnforceAbsence(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted[name]++
	return nil
}

func TestUsersManagement(t *testing.T) {
	// Catch any leaked goroutines. Ignoring goroutines possibly left by other tests.
	leakOpts := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, leakOpts) })

	var client fakeUserMgmtClient
	client.init()

	tmpdir, err := os.MkdirTemp("", "clustermesh-config")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	cfgPath := path.Join(tmpdir, "users.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(users1), 0600))

	hive := hive.New(
		cell.Provide(func() UsersManagementConfig {
			return UsersManagementConfig{
				ClusterUsersEnabled:    true,
				ClusterUsersConfigPath: cfgPath,
			}
		}),

		cell.Provide(func() cmtypes.ClusterInfo {
			return cmtypes.ClusterInfo{ID: 10, Name: "fred"}
		}),

		cell.Provide(func() kvstore.BackendOperationsUserMgmt {
			return &client
		}),

		cell.Invoke(registerUsersManager),
	)

	ctx := t.Context()

	tlog := hivetest.Logger(t)
	if err := hive.Start(tlog, ctx); err != nil {
		t.Fatalf("failed to start: %s", err)
	}

	defer func() {
		if err := hive.Stop(tlog, ctx); err != nil {
			t.Fatalf("failed to stop: %s", err)
		}
	}()

	require.Eventuallyf(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()

		return len(client.created) == 3 && len(client.deleted) == 0
	}, time.Second, 10*time.Millisecond,
		"Failed waiting for events to be triggered: created: %v, deleted: %v",
		client.created, client.deleted)

	require.Equal(t, "r1", client.created["foo"])
	require.Equal(t, "r2", client.created["bar"])
	require.Equal(t, "r3", client.created["qux"])

	client.init()

	// Update the users config file, and require that changes are propagated
	// We first write to a different file and then rename it, to avoid the possible
	// race condition caused by truncate + write if we detect the event sufficiently
	// fast (i.e., we first read an empty file, and then the expected one).
	cfgPath2 := path.Join(tmpdir, "users.yaml.2")
	require.NoError(t, os.WriteFile(cfgPath2, []byte(users2), 0600))
	require.NoError(t, os.Rename(cfgPath2, cfgPath))

	require.Eventuallyf(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()

		return len(client.created) == 2 && len(client.deleted) == 1
	}, time.Second, 10*time.Millisecond,
		"Failed waiting for events to be triggered: created: %v, deleted: %v",
		client.created, client.deleted)

	require.Equal(t, "r3", client.created["baz"])
	require.Equal(t, "r4", client.created["qux"])
	require.Equal(t, 1, client.deleted["bar"])
}
