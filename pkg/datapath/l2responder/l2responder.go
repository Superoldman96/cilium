// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package l2responder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/cilium/hive/cell"
	"github.com/cilium/hive/job"
	"github.com/cilium/statedb"
	"github.com/vishvananda/netlink"

	"github.com/cilium/cilium/pkg/datapath/gneigh"
	"github.com/cilium/cilium/pkg/datapath/linux/safenetlink"
	"github.com/cilium/cilium/pkg/datapath/tables"
	"github.com/cilium/cilium/pkg/ebpf"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/maps/l2respondermap"
	"github.com/cilium/cilium/pkg/maps/l2v6respondermap"
	"github.com/cilium/cilium/pkg/time"
	"github.com/cilium/cilium/pkg/types"
)

// Cell provides the L2 Responder Reconciler. This component takes the desired state, calculated by
// the L2 announcer component from the StateDB table and reconciles it with the L2 responder maps.
// The L2 Responder Reconciler watches for incremental changes in the table and applies these
// incremental changes immediately and it periodically perform full reconciliation as redundancy.
var Cell = cell.Module(
	"l2-responder",
	"L2 Responder Reconciler",

	// Provide and register the Table[*L2AnnounceEntry] containing the
	// desired state.
	cell.Provide(
		tables.NewL2AnnounceTable,
		statedb.RWTable[*tables.L2AnnounceEntry].ToTable,
	),
	cell.Invoke(statedb.RegisterTable[*tables.L2AnnounceEntry]),

	cell.Invoke(NewL2ResponderReconciler),
	cell.Provide(newNeighborNetlink),
)

type params struct {
	cell.In

	Lifecycle           cell.Lifecycle
	Logger              *slog.Logger
	L2AnnouncementTable statedb.RWTable[*tables.L2AnnounceEntry]
	StateDB             *statedb.DB
	L2ResponderMap      l2respondermap.Map
	L2V6ResponderMap    l2v6respondermap.Map
	NetLink             linkByNamer
	JobGroup            job.Group
	Health              cell.Health
	GNeighSender        gneigh.Sender
}

type linkByNamer interface {
	LinkByName(name string) (netlink.Link, error)
}

func newNeighborNetlink() linkByNamer {
	return &netlink.Handle{}
}

type l2ResponderReconciler struct {
	params params
}

func NewL2ResponderReconciler(params params) *l2ResponderReconciler {
	reconciler := l2ResponderReconciler{
		params: params,
	}

	params.JobGroup.Add(job.OneShot("l2-responder-reconciler", reconciler.run))

	return &reconciler
}

func (p *l2ResponderReconciler) run(ctx context.Context, health cell.Health) error {
	log := p.params.Logger

	// This timer triggers full reconciliation once in a while, in case partial reconciliation
	// got out of sync or the map was changed underneath us.
	ticker := time.NewTicker(5 * time.Minute)

	tbl := p.params.L2AnnouncementTable
	txn := p.params.StateDB.WriteTxn(tbl)
	changes, err := tbl.Changes(txn)
	if err != nil {
		txn.Abort()
		return fmt.Errorf("delete tracker: %w", err)
	}
	txn.Commit()

	// At startup, do an initial full reconciliation
	err = p.fullReconciliation(p.params.StateDB.ReadTxn())
	if err != nil {
		log.Error("Error(s) while reconciling l2 responder map", logfields.Error, err)
	}

	for ctx.Err() == nil {
		p.cycle(ctx, changes, ticker.C)
	}

	return nil
}

func (p *l2ResponderReconciler) cycle(
	ctx context.Context,
	changeIter statedb.ChangeIterator[*tables.L2AnnounceEntry],
	fullReconciliation <-chan time.Time,
) {
	arMap := p.params.L2ResponderMap
	ndMap := p.params.L2V6ResponderMap
	log := p.params.Logger

	lr := cachingLinkResolver{nl: p.params.NetLink}

	process := func(e *tables.L2AnnounceEntry, deleted bool) error {

		idx, err := lr.LinkIndex(e.NetworkInterface)
		if err != nil {
			return fmt.Errorf("link index: %w", err)
		}

		if deleted {
			if e.IP.Is6() {
				err = ndMap.Delete(e.IP, uint32(idx))
			} else {
				err = arMap.Delete(e.IP, uint32(idx))
			}

			if err != nil {
				return fmt.Errorf("delete %s@%d: %w", e.IP, idx, err)
			}

			return nil
		}

		if e.IP.Is6() {
			err = gneighOnNewEntry(ndMap, p.params.GNeighSender, e.IP, idx)
		} else {
			err = garpOnNewEntry(arMap, p.params.GNeighSender, e.IP, idx)
		}
		if err != nil {
			log.Warn("Unable to send gratuitous ARP/NDP. Continuing...", logfields.Error, err)
		}

		if e.IP.Is6() {
			err = ndMap.Create(e.IP, uint32(idx))
		} else {
			err = arMap.Create(e.IP, uint32(idx))
		}
		if err != nil {
			return fmt.Errorf("create %s@%d: %w", e.IP, idx, err)
		}

		return nil
	}

	// Partial reconciliation
	txn := p.params.StateDB.ReadTxn()
	changes, watch := changeIter.Next(txn)
	for change := range changes {
		err := process(change.Object, change.Deleted)
		if err != nil {
			log.Error("error during partial reconciliation", logfields.Error, err)
			break
		}
	}

	select {
	case <-ctx.Done():
		// Shutdown
		return

	case <-watch:
		// There are pending changes in the table, return from the cycle

	case <-fullReconciliation:
		// Full reconciliation timer fired, perform full reconciliation

		// The existing `iter` is the result of a `All` query, so this will return all
		// entries in the table for full reconciliation.
		err := p.fullReconciliation(txn)
		if err != nil {
			log.Error("Error(s) while full reconciling l2 responder map", logfields.Error, err)
		}
	}
}

func (p *l2ResponderReconciler) fullReconciliation(txn statedb.ReadTxn) (err error) {
	var errs error

	log := p.params.Logger
	tbl := p.params.L2AnnouncementTable
	arMap := p.params.L2ResponderMap
	ndMap := p.params.L2V6ResponderMap
	lr := cachingLinkResolver{nl: p.params.NetLink}

	log.Debug("l2 announcer table full reconciliation")

	// Prepare index for desired entries based on map key
	type desiredEntry struct {
		satisfied bool
		entry     *tables.L2AnnounceEntry
	}
	desiredMap := make(map[l2respondermap.L2ResponderKey]desiredEntry)
	desiredMap6 := make(map[l2v6respondermap.L2V6ResponderKey]desiredEntry)

	for e := range tbl.All(txn) {
		idx, err := lr.LinkIndex(e.NetworkInterface)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		if e.IP.Is6() {
			desiredMap6[l2v6respondermap.L2V6ResponderKey{
				IP:      types.IPv6(e.IP.As16()),
				IfIndex: uint32(idx),
				Pad:     uint32(0),
			}] = desiredEntry{
				entry: e,
			}
		} else {
			desiredMap[l2respondermap.L2ResponderKey{
				IP:      types.IPv4(e.IP.As4()),
				IfIndex: uint32(idx),
			}] = desiredEntry{
				entry: e,
			}
		}
	}

	// Loop over all map values, use the desired entries index to see which we want to delete.
	var toDelete []*l2respondermap.L2ResponderKey
	arMap.IterateWithCallback(func(key *l2respondermap.L2ResponderKey, _ *l2respondermap.L2ResponderStats) {
		e, found := desiredMap[*key]
		if !found {
			toDelete = append(toDelete, key)
			return
		}
		e.satisfied = true
		desiredMap[*key] = e
	})
	var toDelete6 []*l2v6respondermap.L2V6ResponderKey
	ndMap.IterateWithCallback(func(key *l2v6respondermap.L2V6ResponderKey, _ *l2respondermap.L2ResponderStats) {
		e, found := desiredMap6[*key]
		if !found {
			toDelete6 = append(toDelete6, key)
			return
		}
		e.satisfied = true
		desiredMap6[*key] = e
	})

	// Delete all unwanted map values
	for _, del := range toDelete {
		if err := arMap.Delete(netip.AddrFrom4(del.IP), del.IfIndex); err != nil {
			errs = errors.Join(errs, fmt.Errorf("delete %s@%d: %w", del.IP, del.IfIndex, err))
		}
	}
	for _, del := range toDelete6 {
		if err := ndMap.Delete(netip.AddrFrom16(del.IP), del.IfIndex); err != nil {
			errs = errors.Join(errs, fmt.Errorf("delete %s@%d: %w", del.IP, del.IfIndex, err))
		}
	}

	// Add map values that do not yet exist
	for key, entry := range desiredMap {
		if entry.satisfied {
			continue
		}

		err = garpOnNewEntry(arMap, p.params.GNeighSender, netip.AddrFrom4(key.IP), int(key.IfIndex))
		if err != nil {
			errs = errors.Join(errs, err)
		}

		if err := arMap.Create(netip.AddrFrom4(key.IP), key.IfIndex); err != nil {
			errs = errors.Join(errs, fmt.Errorf("create %s@%d: %w", key.IP, key.IfIndex, err))
		}
	}
	for key, entry := range desiredMap6 {
		if entry.satisfied {
			continue
		}

		err = gneighOnNewEntry(ndMap, p.params.GNeighSender, netip.AddrFrom16(key.IP), int(key.IfIndex))
		if err != nil {
			errs = errors.Join(errs, err)
		}

		if err := ndMap.Create(netip.AddrFrom16(key.IP), key.IfIndex); err != nil {
			errs = errors.Join(errs, fmt.Errorf("create %s@%d: %w", key.IP, key.IfIndex, err))
		}
	}
	return errs
}

// If the given IP and network interface index does not yet exist in the l2 responder map,
// a failover might have taken place. Therefor we should send out a gARP reply to let
// the local network know the IP has moved to minimize downtime due to ARP caching.
func garpOnNewEntry(arMap l2respondermap.Map, sender gneigh.Sender, ip netip.Addr, ifIndex int) error {
	_, err := arMap.Lookup(ip, uint32(ifIndex))
	if !errors.Is(err, ebpf.ErrKeyNotExist) {
		return nil
	}

	iface, err := sender.InterfaceByIndex(ifIndex)
	if err != nil {
		return fmt.Errorf("garp %s@%d: %w", ip, ifIndex, err)
	}

	err = sender.SendArp(iface, ip)
	if err != nil {
		return fmt.Errorf("garp %s@%d: %w", ip, ifIndex, err)
	}

	return nil
}

func gneighOnNewEntry(ndMap l2v6respondermap.Map, sender gneigh.Sender, ip netip.Addr, ifIndex int) error {
	_, err := ndMap.Lookup(ip, uint32(ifIndex))
	if !errors.Is(err, ebpf.ErrKeyNotExist) {
		return nil
	}

	iface, err := sender.InterfaceByIndex(ifIndex)
	if err != nil {
		return fmt.Errorf("gneigh adv %s@%d: %w", ip, ifIndex, err)
	}

	err = sender.SendNd(iface, ip)
	if err != nil {
		return fmt.Errorf("gneigh adv %s@%d: %w", ip, ifIndex, err)
	}

	return nil
}

type cachingLinkResolver struct {
	nl    linkByNamer
	cache map[string]int
}

// LinkIndex returns the link index for a given netdev name, from its cache or netlink
func (clr *cachingLinkResolver) LinkIndex(name string) (int, error) {
	if clr.cache == nil {
		clr.cache = make(map[string]int)
	}

	idx, found := clr.cache[name]
	if found {
		return idx, nil
	}

	link, err := safenetlink.WithRetryResult(func() (netlink.Link, error) {
		return clr.nl.LinkByName(name)
	})
	if err != nil {
		return 0, err
	}

	idx = link.Attrs().Index
	clr.cache[name] = idx

	return idx, nil
}
