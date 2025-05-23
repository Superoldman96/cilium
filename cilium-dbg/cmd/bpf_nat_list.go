// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package cmd

import (
	"fmt"
	"os"
	"reflect"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/cilium/cilium/pkg/bpf"
	"github.com/cilium/cilium/pkg/command"
	"github.com/cilium/cilium/pkg/common"
	"github.com/cilium/cilium/pkg/maps/nat"
	"github.com/cilium/cilium/pkg/maps/timestamp"
)

// bpfNatListCmd represents the bpf_nat_list command
var bpfNatListCmd = &cobra.Command{
	Use:     "list [cluster <cluster id>]",
	Aliases: []string{"ls"},
	Short:   "List all NAT mapping entries",
	Run: func(cmd *cobra.Command, args []string) {
		common.RequireRootPrivilege("cilium bpf nat list")
		if len(args) == 0 {
			ipv4, ipv6 := getIpEnableStatuses()
			ipv4Map, ipv6Map := nat.GlobalMaps(nil, ipv4, ipv6, true)
			globalMaps := make([]nat.NatMap, 2)
			globalMaps[0] = ipv4Map
			globalMaps[1] = ipv6Map
			dumpNat(globalMaps)
		} else if len(args) == 2 && args[0] == "cluster" {
			clusterID, err := strconv.ParseUint(args[1], 10, 32)
			if err != nil {
				cmd.PrintErrf("Invalid ClusterID: %s", err.Error())
				return
			}
			ipv4, ipv6 := getIpEnableStatuses()
			ipv4Map, ipv6Map, err := nat.ClusterMaps(uint32(clusterID), ipv4, ipv6)
			if err != nil {
				cmd.PrintErrf("Failed to retrieve cluster maps: %s", err.Error())
				return
			}
			clusterMaps := make([]nat.NatMap, 2)
			clusterMaps[0] = ipv4Map
			clusterMaps[1] = ipv6Map
			dumpNat(clusterMaps)
		} else {
			cmd.PrintErr("Invalid argument")
			return
		}
	},
}

func init() {
	BPFNatCmd.AddCommand(bpfNatListCmd)
	command.AddOutputOption(bpfNatListCmd)
}

func dumpNat(maps []nat.NatMap, args ...any) {
	entries := make([]nat.NatMapRecord, 0)

	for _, m := range maps {
		if m == nil || reflect.ValueOf(m).IsNil() {
			continue
		}
		path, err := m.Path()
		if err == nil {
			err = m.Open()
		}
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "Unable to open %s: %s. Skipping.\n", path, err)
				continue
			}
			Fatalf("Unable to open %s: %s", path, err)
		}
		defer m.Close()
		// Plain output prints immediately, JSON/YAML output holds until it
		// collected values from all maps to have one consistent object
		if command.OutputOption() {
			callback := func(key bpf.MapKey, value bpf.MapValue) {
				record := nat.NatMapRecord{Key: key.(nat.NatKey), Value: value.(nat.NatEntry)}
				entries = append(entries, record)
			}
			if err = m.DumpWithCallback(callback); err != nil {
				Fatalf("Error while collecting BPF map entries: %s", err)
			}
		} else {
			clockSource, err := timestamp.GetClockSourceFromAgent(client.Daemon)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to get clocksource from agent: %s", err)
				clockSource, err = timestamp.GetClockSourceFromRuntimeConfig()
			}
			if err != nil {
				Fatalf("Error while dumping BPF Map: %s", err)
			}
			out, err := nat.DumpEntriesWithTimeDiff(m, clockSource)
			if err != nil {
				Fatalf("Error while dumping BPF Map: %s", err)
			}
			fmt.Println(out)
		}
	}
	if command.OutputOption() {
		if err := command.PrintOutput(entries); err != nil {
			os.Exit(1)
		}
	}
}
