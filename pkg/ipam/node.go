// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

// Copyright 2017 Lyft, Inc.

package ipam

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync/atomic"

	"k8s.io/client-go/tools/cache"

	operatorK8s "github.com/cilium/cilium/operator/k8s"
	operatorOption "github.com/cilium/cilium/operator/option"
	"github.com/cilium/cilium/operator/watchers"
	"github.com/cilium/cilium/pkg/defaults"
	"github.com/cilium/cilium/pkg/ipam/metrics"
	ipamOption "github.com/cilium/cilium/pkg/ipam/option"
	ipamTypes "github.com/cilium/cilium/pkg/ipam/types"
	v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	v1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/api/core/v1"
	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/logging"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/time"
	"github.com/cilium/cilium/pkg/trigger"
)

const (
	// warningInterval is the interval for warnings which should be done
	// once and then repeated if the warning persists.
	warningInterval = time.Hour

	// allocation type
	createInterfaceAndAllocateIP = "createInterfaceAndAllocateIP"
	allocateIP                   = "allocateIP"
	releaseIP                    = "releaseIP"
	releaseIPPrefixes            = "releaseIPPrefixes"

	// operator status
	success = "success"
	failed  = "failed"
)

func (n *Node) SetOpts(ops NodeOperations) {
	n.ops = ops
}

func (n *Node) SetPoolMaintainer(maintainer PoolMaintainer) {
	n.poolMaintainer = maintainer
}

type PoolMaintainer interface {
	Trigger()
	Shutdown()
}

// Node represents a Kubernetes node running Cilium with an associated
// CiliumNode custom resource
type Node struct {
	rootLogger *slog.Logger
	logger     atomic.Pointer[slog.Logger]

	// mutex protects all members of this structure
	mutex lock.RWMutex

	// name is the name of the node
	name string

	// resource is the link to the CiliumNode custom resource
	resource *v2.CiliumNode

	// stats provides accounting for various per node statistics
	stats Statistics

	// lastMaxAdapterWarning is the timestamp when the last warning was
	// printed that this node is out of adapters
	lastMaxAdapterWarning time.Time

	// instanceRunning is true when the EC2 instance backing the node is
	// not running. This state is detected based on error messages returned
	// when modifying instance state
	instanceRunning bool

	// instanceStoppedRunning records when an instance was most recently set to not running
	instanceStoppedRunning time.Time

	// ipv4Alloc represents IPv4-specific allocation attributes for this node
	ipv4Alloc ipAllocAttrs

	// TODO: Add support for IPv6 allocation: https://github.com/cilium/cilium/issues/19251

	// resyncNeeded is set to the current time when a resync with the EC2
	// API is required. The timestamp is required to ensure that this is
	// only reset if the resync started after the time stored in
	// resyncNeeded. This is needed because resyncs and allocations happen
	// in parallel.
	resyncNeeded time.Time

	// manager is the NodeManager responsible for this node
	manager *NodeManager

	// poolMaintainer is the trigger used to assign/unassign
	// private IP addresses of this node.
	// It ensures that multiple requests to operate private IPs are
	// batched together if pool maintenance is still ongoing.
	poolMaintainer PoolMaintainer

	// k8sSync is the trigger used to synchronize node information with the
	// K8s apiserver. The trigger is used to batch multiple updates
	// together if the apiserver is slow to respond or subject to rate
	// limiting.
	k8sSync *trigger.Trigger

	// instanceSync is the trigger used to fetch instance information
	// with external APIs or systems.
	instanceSync *trigger.Trigger

	// ops is the IPAM implementation to used for this node
	ops NodeOperations

	// retry is the trigger used to retry pool maintenance while the
	// instances API is unstable
	retry *trigger.Trigger

	// logLimiter rate limits potentially repeating warning logs
	logLimiter logging.Limiter
}

// ipAllocAttrs represents IP-specific allocation attributes.
type ipAllocAttrs struct {
	// waitingForPoolMaintenance is true when the node is subject to an
	// IP address allocation or release which must be performed before
	// another allocation or release can be attempted
	waitingForPoolMaintenance bool

	// available is the map of IP addresses available to this node
	available ipamTypes.AllocationMap

	// Excess IP address from a cilium node would be marked for release only after a delay
	// configured by excess-ip-release-delay flag. ipsMarkedForRelease tracks the IP and the
	// timestamp at which it was marked for release.
	ipsMarkedForRelease map[string]time.Time

	// ipReleaseStatus tracks the state for every IP address considered for release.
	// IPAMMarkForRelease  : Marked for Release
	// IPAMReadyForRelease : Acknowledged as safe to release by agent
	// IPAMDoNotRelease    : Release request denied by agent
	// IPAMReleased        : IP released by the operator
	ipReleaseStatus map[string]string
}

// Statistics represent the IP allocation statistics of a node
type Statistics struct {
	// IPv4 represents IPv4-specific statistics.
	IPv4 IPStatistics

	// IPv6 represents IPv6-specific statistics.
	IPv6 IPStatistics

	// EmptyInterfaceSlots is the number of empty interface slots available
	// for interfaces to be attached.
	EmptyInterfaceSlots int
}

// IPStatistics represents IP-specific allocation statistics.
type IPStatistics struct {
	// UsedIPs is the number of IPs currently in use
	UsedIPs int

	// AvailableIPs is the number of IPs currently allocated and available for assignment.
	AvailableIPs int

	// Capacity is the max inferred IPAM IP capacity for the node.
	// In theory, this provides an upper limit on the number of Cilium IPs that
	// this Node can support.
	Capacity int

	// NeededIPs is the number of IPs needed to reach the PreAllocate
	// watermwark
	NeededIPs int

	// ExcessIPs is the number of free IPs exceeding MaxAboveWatermark
	ExcessIPs int

	// RemainingInterfaces is the number of interfaces that can either be
	// allocated or have not yet exhausted the instance specific quota of
	// addresses
	RemainingInterfaces int

	// InterfaceCandidates is the number of attached interfaces with IPs
	// available for allocation.
	InterfaceCandidates int

	// AssignedStaticIP is the static IP address assigned to the node (ex: public Elastic IP address in AWS)
	AssignedStaticIP string
}

// IsRunning returns true if the node is considered to be running
func (n *Node) IsRunning() bool {
	n.mutex.RLock()
	defer n.mutex.RUnlock()
	return n.instanceRunning
}

func (n *Node) SetRunning(running bool) {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	n.logger.Load().Info(fmt.Sprintf("Set running %t", running))
	n.instanceRunning = running
	if !n.instanceRunning {
		n.instanceStoppedRunning = time.Now()
	}
}

// Stats returns a copy of the node statistics
func (n *Node) Stats() Statistics {
	n.mutex.RLock()
	c := n.stats
	n.mutex.RUnlock()
	return c
}

// Ops returns the IPAM implementation operations for the node
func (n *Node) Ops() NodeOperations {
	return n.ops
}

func (n *Node) IsPrefixDelegationEnabled() bool {
	return n.manager.prefixDelegation
}

func (n *Node) updateLogger() {
	if n.resource != nil {
		n.logger.Store(n.rootLogger.With(
			fieldName, n.name,
			logfields.InstanceID, n.resource.InstanceID(),
		))
	}
}

// getMaxAboveWatermark returns the max-above-watermark setting for an AWS node
//
// n.mutex must be held when calling this function
func (n *Node) getMaxAboveWatermark() int {
	return n.resource.Spec.IPAM.MaxAboveWatermark
}

// getPreAllocate returns the pre-allocation setting for an AWS node
//
// n.mutex must be held when calling this function
func (n *Node) getPreAllocate() int {
	if n.resource.Spec.IPAM.PreAllocate != 0 {
		return n.resource.Spec.IPAM.PreAllocate
	}
	return defaults.IPAMPreAllocation
}

// getMinAllocate returns the minimum-allocation setting of an AWS node
//
// n.mutex must be held when calling this function
func (n *Node) getMinAllocate() int {
	return n.resource.Spec.IPAM.MinAllocate
}

// getMaxAllocate returns the maximum-allocation setting of an AWS node
func (n *Node) getMaxAllocate() int {
	instanceMax := n.ops.GetMaximumAllocatableIPv4()
	if n.resource.Spec.IPAM.MaxAllocate > 0 {
		if n.resource.Spec.IPAM.MaxAllocate > instanceMax {
			n.logger.Load().Warn(
				fmt.Sprintf("max-allocate (%d) is higher than the instance type limits (%d)",
					n.resource.Spec.IPAM.MaxAllocate,
					instanceMax),
			)
		}
		return n.resource.Spec.IPAM.MaxAllocate
	}

	return instanceMax
}

func (n *Node) getStaticIPTags() ipamTypes.Tags {
	if n.resource.Spec.IPAM.StaticIPTags != nil {
		return n.resource.Spec.IPAM.StaticIPTags
	} else {
		return ipamTypes.Tags{}
	}
}

// GetNeededAddresses returns the number of needed addresses that need to be
// allocated or released. A positive number is returned to indicate allocation.
// A negative number is returned to indicate release of addresses.
func (n *Node) GetNeededAddresses() int {
	stats := n.Stats()
	if stats.IPv4.NeededIPs > 0 {
		return stats.IPv4.NeededIPs
	}
	if n.manager.releaseExcessIPs && stats.IPv4.ExcessIPs > 0 {
		// Nodes are sorted by needed addresses, return negative values of excessIPs
		// so that nodes with IP deficit are resolved first
		return stats.IPv4.ExcessIPs * -1
	}
	return 0
}

// getPendingPodCount computes the number of pods in pending state on a given node. watchers.PodStore is assumed to be
// initialized before this function is called.
func getPendingPodCount(nodeName string) (int, error) {
	pendingPods := 0
	if watchers.PodStore == nil {
		return pendingPods, fmt.Errorf("pod store uninitialized")
	}
	values, err := watchers.PodStore.(cache.Indexer).ByIndex(operatorK8s.PodNodeNameIndex, nodeName)
	if err != nil {
		return pendingPods, fmt.Errorf("unable to access pod to node name index: %w", err)
	}
	for _, pod := range values {
		p := pod.(*v1.Pod)
		if p.Status.Phase == v1.PodPending {
			pendingPods++
		}
	}
	return pendingPods, nil
}

func calculateNeededIPs(availableIPs, usedIPs, preAllocate, minAllocate, maxAllocate int) (neededIPs int) {
	neededIPs = preAllocate - (availableIPs - usedIPs)

	if minAllocate > 0 {
		neededIPs = max(neededIPs, minAllocate-availableIPs)
	}

	// If maxAllocate is set (> 0) and neededIPs is higher than the
	// maxAllocate value, we only return the amount of IPs that can
	// still be allocated
	if maxAllocate > 0 && (availableIPs+neededIPs) > maxAllocate {
		neededIPs = maxAllocate - availableIPs
	}

	if neededIPs < 0 {
		neededIPs = 0
	}
	return
}

func calculateExcessIPs(availableIPs, usedIPs, preAllocate, minAllocate, maxAboveWatermark int) (excessIPs int) {
	// keep availableIPs above minAllocate + maxAboveWatermark as long as
	// the initial socket of min-allocate + max-above-watermark has not
	// been used up yet. This is the maximum potential allocation that will
	// happen on initial bootstrap.  Depending on interface restrictions,
	// the actual allocation may be below this but we always want to avoid
	// releasing IPs that have just been allocated.
	if usedIPs <= (minAllocate + maxAboveWatermark) {
		if availableIPs <= (minAllocate + maxAboveWatermark) {
			return 0
		}

		// if usedIPs+preAllocate not over minAllocate + maxAboveWatermark, only care
		// the ips out of minAllocate + maxAboveWatermark
		if (usedIPs + preAllocate) <= (minAllocate + maxAboveWatermark) {
			return availableIPs - minAllocate - maxAboveWatermark
		}
	}

	// Once above the minimum allocation level, calculate based on
	// pre-allocation limit with the max-above-watermark limit calculated
	// in. This is again a best-effort calculation, depending on the
	// interface restrictions, less than max-above-watermark may have been
	// allocated but we never want to release IPs that have been allocated
	// because of max-above-watermark.
	excessIPs = max(availableIPs-usedIPs-preAllocate-maxAboveWatermark, 0)
	return
}

func (n *Node) requirePoolMaintenance() {
	n.mutex.Lock()
	n.ipv4Alloc.waitingForPoolMaintenance = true
	n.mutex.Unlock()
}

func (n *Node) poolMaintenanceComplete() {
	n.mutex.Lock()
	n.ipv4Alloc.waitingForPoolMaintenance = false
	n.mutex.Unlock()
}

// InstanceID returns the instance ID of the node
func (n *Node) InstanceID() (id string) {
	n.mutex.RLock()
	if n.resource != nil {
		id = n.resource.InstanceID()
	}
	n.mutex.RUnlock()
	return
}

func (n *Node) instanceAPISync(ctx context.Context, instanceID string) (time.Time, bool) {
	syncTime := n.manager.instancesAPI.InstanceSync(ctx, instanceID)
	success := !syncTime.IsZero()
	return syncTime, success
}

// UpdatedResource is called when an update to the CiliumNode has been
// received. The IPAM layer will attempt to immediately resolve any IP deficits
// and also trigger the background sync to continue working in the background
// to resolve any deficits or excess.
func (n *Node) UpdatedResource(resource *v2.CiliumNode) bool {
	// Deep copy the resource before storing it. This way we are not
	// dependent on caller not using the resource after this call.
	resource = resource.DeepCopy()

	// Update n.resource before n.ops.UpdatedNode. This is to increase the chance
	// of performing a complete n.recalculate() execution when the nodeManager performs a resync
	// and the operator is starting up.
	// This is best effort to solve the issue where the metrics `cilium_operator_ipam_available_interfaces`
	// only contains a part of the nodes at operator startup. It will be set to the correct
	// value after the next period where nodeManager performs a resync.
	n.mutex.Lock()
	// Any modification to the custom resource is seen as a sign that the
	// instance is alive
	n.instanceRunning = true
	n.resource = resource
	n.mutex.Unlock()
	n.updateLogger()

	n.ops.UpdatedNode(resource)

	n.recalculate(context.Background())
	allocationNeeded := n.allocationNeeded()
	if allocationNeeded {
		n.requirePoolMaintenance()
		n.poolMaintainer.Trigger()
	}

	return allocationNeeded
}

func (n *Node) resourceAttached() (attached bool) {
	n.mutex.RLock()
	attached = n.resource != nil
	n.mutex.RUnlock()
	return
}

func (n *Node) recalculate(ctx context.Context) {
	// Skip any recalculation if the CiliumNode resource does not exist yet
	if !n.resourceAttached() {
		return
	}
	scopedLog := n.logger.Load()

	a, stats, err := n.ops.ResyncInterfacesAndIPs(ctx, scopedLog)

	n.mutex.Lock()
	defer n.mutex.Unlock()

	if err != nil {
		var limitsNotFound LimitsNotFound
		ok := errors.As(err, &limitsNotFound)
		if ok {
			scopedLog.Warn("Instance limits not found.", logfields.Error, err)
		} else {
			scopedLog.Warn("Instance not found! Please delete corresponding ciliumnode if instance has already been deleted.", logfields.Error, err)
		}
		// Avoid any further action
		n.stats.IPv4.NeededIPs = 0
		n.stats.IPv4.ExcessIPs = 0
		return
	}

	n.ipv4Alloc.available = a
	n.stats.IPv4.UsedIPs = len(n.resource.Status.IPAM.Used)
	if stats.AssignedStaticIP != "" {
		n.stats.IPv4.AssignedStaticIP = stats.AssignedStaticIP
	}

	n.stats.IPv4.AvailableIPs = len(n.ipv4Alloc.available)
	n.stats.IPv4.NeededIPs = calculateNeededIPs(n.stats.IPv4.AvailableIPs, n.stats.IPv4.UsedIPs, n.getPreAllocate(), n.getMinAllocate(), n.getMaxAllocate())
	n.stats.IPv4.ExcessIPs = calculateExcessIPs(n.stats.IPv4.AvailableIPs, n.stats.IPv4.UsedIPs, n.getPreAllocate(), n.getMinAllocate(), n.getMaxAboveWatermark())
	n.stats.IPv4.RemainingInterfaces = stats.RemainingAvailableInterfaceCount
	n.stats.IPv4.Capacity = stats.NodeCapacity
	scopedLog.Debug(
		"Recalculated needed addresses",
		logfields.Available, n.stats.IPv4.AvailableIPs,
		logfields.Capacity, n.stats.IPv4.Capacity,
		logfields.Used, n.stats.IPv4.UsedIPs,
		logfields.ToAllocate, n.stats.IPv4.NeededIPs,
		logfields.ToRelease, n.stats.IPv4.ExcessIPs,
		logfields.WaitingForPoolMaintenance, n.ipv4Alloc.waitingForPoolMaintenance,
		logfields.ResyncNeeded, n.resyncNeeded,
		logfields.RemainingInterfaces, stats.RemainingAvailableInterfaceCount,
	)
}

// allocationNeeded returns true if this node requires IPs to be allocated
func (n *Node) allocationNeeded() (needed bool) {
	n.mutex.RLock()
	needed = !n.ipv4Alloc.waitingForPoolMaintenance && n.resyncNeeded.IsZero() && n.stats.IPv4.NeededIPs > 0
	n.mutex.RUnlock()
	return
}

// releaseNeeded returns true if this node requires IPs to be released
func (n *Node) releaseNeeded() (needed bool) {
	n.mutex.RLock()
	needed = n.manager.releaseExcessIPs && !n.ipv4Alloc.waitingForPoolMaintenance && n.resyncNeeded.IsZero() && n.stats.IPv4.ExcessIPs > 0
	if n.resource != nil {
		releaseInProgress := len(n.resource.Status.IPAM.ReleaseIPs) > 0
		needed = needed || releaseInProgress
	}
	n.mutex.RUnlock()
	return
}

// Pool returns the IP allocation pool available to the node
func (n *Node) Pool() (pool ipamTypes.AllocationMap) {
	pool = ipamTypes.AllocationMap{}
	n.mutex.RLock()
	maps.Copy(pool, n.ipv4Alloc.available)
	n.mutex.RUnlock()
	return
}

// ResourceCopy returns a deep copy of the CiliumNode custom resource
// associated with the node
func (n *Node) ResourceCopy() *v2.CiliumNode {
	n.mutex.RLock()
	defer n.mutex.RUnlock()
	return n.resource.DeepCopy()
}

// createInterface creates an additional interface with the instance and
// attaches it to the instance as specified by the CiliumNode. neededAddresses
// of secondary IPs are assigned to the interface up to the maximum number of
// addresses as allowed by the instance.
func (n *Node) createInterface(ctx context.Context, a *AllocationAction) (created bool, err error) {
	if a.EmptyInterfaceSlots == 0 {
		// This is not a failure scenario, warn once per hour but do
		// not track as interface allocation failure. There is a
		// separate metric to track nodes running at capacity.
		n.mutex.Lock()
		if time.Since(n.lastMaxAdapterWarning) > warningInterval {
			n.logger.Load().Warn("Instance is out of interfaces")
			n.lastMaxAdapterWarning = time.Now()
		}
		n.mutex.Unlock()
		return false, nil
	}

	start := time.Now()
	toAllocate, errCondition, err := n.ops.CreateInterface(ctx, a, n.logger.Load())
	if err != nil {
		n.manager.metricsAPI.AllocationAttempt(createInterfaceAndAllocateIP, errCondition, string(a.PoolID), metrics.SinceInSeconds(start))
		n.logger.Load().Warn(
			"Unable to create interface on instance",
			logfields.Error, err,
		)
		return false, err
	}

	n.manager.metricsAPI.AllocationAttempt(createInterfaceAndAllocateIP, success, string(a.PoolID), metrics.SinceInSeconds(start))
	n.manager.metricsAPI.AddIPAllocation(string(a.PoolID), int64(toAllocate))
	n.manager.metricsAPI.IncInterfaceAllocation(string(a.PoolID))

	return true, nil
}

// AllocationAction is the action to be taken to resolve allocation deficits
// for a particular node. It is returned by
// NodeOperations.PrepareIPAllocation() and passed into
// NodeOperations.AllocateIPs().
type AllocationAction struct {
	// InterfaceID is set to the identifier describing the interface on
	// which the IPs must be allocated. This is optional, an IPAM
	// implementation can leave this empty to indicate that no interface
	// context is needed or a new interface must be created.
	InterfaceID string

	// Interface is the interface to allocate IPs on
	Interface ipamTypes.InterfaceRevision

	// PoolID is the IPAM pool identifier to allocate the IPs from. This
	// can correspond to a subnet ID or it can also left blank or set to a
	// value such as "global" to indicate a single address pool.
	PoolID ipamTypes.PoolID

	// EmptyInterfaceSlots is the number of empty interface slots available
	// for interfaces to be attached.
	EmptyInterfaceSlots int

	// IPv4 represents IPv4-specific allocation actions.
	IPv4 IPAllocationAction
}

// IPAllocationAction is the IP-specific action to be taken to resolve allocation deficits
// for a particular node.
type IPAllocationAction struct {
	// AvailableForAllocation is the number IPs available for allocation.
	// If InterfaceID is set, then this number corresponds to the number of
	// IPs available for allocation on that interface. This number may be
	// lower than the number of IPs required to resolve the deficit.
	AvailableForAllocation int

	// MaxIPsToAllocate is set by the core IPAM layer before
	// NodeOperations.AllocateIPs() is called and defines the maximum
	// number of IPs to allocate in order to stay within the boundaries as
	// defined by NodeOperations.{ MinAllocate() | PreAllocate() |
	// getMaxAboveWatermark() }.
	MaxIPsToAllocate int

	// InterfaceCandidates is the number of attached interfaces with IPs
	// available for allocation.
	InterfaceCandidates int
}

// ReleaseAction is the action to be taken to resolve allocation excess for a
// particular node. It is returned by NodeOperations.PrepareIPRelease() and
// passed into NodeOperations.ReleaseIPs().
type ReleaseAction struct {
	// InterfaceID is set to the identifier describing the interface on
	// which the IPs must be released. This is optional, an IPAM
	// implementation can leave this empty to indicate that no interface
	// context is needed.
	InterfaceID string

	// PoolID is the IPAM pool identifier to release the IPs from. This can
	// correspond to a subnet ID or it can also left blank or set to a
	// value such as "global" to indicate a single address pool.
	PoolID ipamTypes.PoolID

	// IPsToRelease is the list of IPs to release
	IPsToRelease []string

	// IPPrefixes is the list of prefixes to release
	IPPrefixesToRelease []string
}

// maintenanceAction represents the resources available for allocation for a
// particular ciliumNode. If an existing interface has IP allocation capacity
// left, that capacity is used up first. If not, an available index is found to
// create a new interface.
type maintenanceAction struct {
	allocation *AllocationAction
	release    *ReleaseAction
}

func (n *Node) determineMaintenanceAction() (*maintenanceAction, error) {
	var err error

	a := &maintenanceAction{}

	stats := n.Stats()
	// Validate that the node still requires addresses to be released, the
	// request may have been resolved in the meantime.
	if n.manager.releaseExcessIPs && stats.IPv4.ExcessIPs > 0 {
		a.release = n.ops.PrepareIPRelease(stats.IPv4.ExcessIPs, n.logger.Load())
		if a.release != nil && len(a.release.IPsToRelease) > 0 {
			return a, nil
		}
	}

	// Validate that the node still requires addresses to be allocated, the
	// request may have been resolved in the meantime.
	if stats.IPv4.NeededIPs == 0 {
		return nil, nil
	}

	a.allocation, err = n.ops.PrepareIPAllocation(n.logger.Load())
	if err != nil {
		return nil, err
	}

	surgeAllocate := 0
	numPendingPods, err := getPendingPodCount(n.name)
	if err != nil {
		if n.logLimiter.Allow() {
			n.logger.Load().Warn(
				"Unable to compute pending pods, will not surge-allocate",
				logfields.Error, err,
			)
		}
	} else if numPendingPods > stats.IPv4.NeededIPs {
		surgeAllocate = numPendingPods - stats.IPv4.NeededIPs
	}

	n.mutex.RLock()
	// handleIPAllocation() takes a min of MaxIPsToAllocate and IPs available for allocation on the interface.
	// This makes sure we don't try to allocate more than what's available.
	a.allocation.IPv4.MaxIPsToAllocate = stats.IPv4.NeededIPs + n.getMaxAboveWatermark() + surgeAllocate
	n.mutex.RUnlock()

	scopedLog := n.logger.Load()
	if a.allocation != nil {
		n.mutex.Lock()
		n.stats.IPv4.RemainingInterfaces = a.allocation.IPv4.InterfaceCandidates + a.allocation.EmptyInterfaceSlots
		stats = n.stats
		n.mutex.Unlock()
		scopedLog = n.logger.Load().With(
			logfields.SelectedInterface, a.allocation.InterfaceID,
			logfields.SelectedPoolID, a.allocation.PoolID,
			logfields.MaxIPsToAllocate, a.allocation.IPv4.MaxIPsToAllocate,
			logfields.AvailableForAllocation, a.allocation.IPv4.AvailableForAllocation,
			logfields.EmptyInterfaceSlots, a.allocation.EmptyInterfaceSlots,
		)
	}

	scopedLog.Info(
		"Resolving IP deficit of node",
		logfields.Available, stats.IPv4.AvailableIPs,
		logfields.Used, stats.IPv4.UsedIPs,
		logfields.NeededIPs, stats.IPv4.NeededIPs,
		logfields.RemainingInterfaces, stats.IPv4.RemainingInterfaces,
	)

	return a, nil
}

// removeStaleReleaseIPs Removes stale entries in local n.ipReleaseStatus. Once the handshake is complete agent would
// remove entries from IP release status map in ciliumnode CRD's status. These IPs need to be purged from
// n.ipReleaseStatus
func (n *Node) removeStaleReleaseIPs() {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	for ip, status := range n.ipv4Alloc.ipReleaseStatus {
		if status != ipamOption.IPAMReleased {
			continue
		}
		if _, ok := n.resource.Status.IPAM.ReleaseIPs[ip]; !ok {
			delete(n.ipv4Alloc.ipReleaseStatus, ip)
		}
	}
}

// abortNoLongerExcessIPs allows for aborting release of IP if new allocations on the node result in a change of excess
// count or the interface selected for release.
func (n *Node) abortNoLongerExcessIPs(excessMap map[string]bool) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	if len(n.resource.Status.IPAM.ReleaseIPs) == 0 {
		return
	}
	for ip, status := range n.resource.Status.IPAM.ReleaseIPs {
		if excessMap[ip] {
			continue
		}
		// Handshake can be aborted from every state except 'released'
		// 'released' state is removed by the agent once the IP has been removed from ciliumnode's IPAM pool as well.
		// But if the IP is back in the pool, we need to remove it from the release status map.
		if status == ipamOption.IPAMReleased {
			// Check if the IP is back in the pool despite being marked as released
			if _, ok := n.resource.Spec.IPAM.Pool[ip]; ok {
				delete(n.resource.Status.IPAM.ReleaseIPs, ip)
				delete(n.ipv4Alloc.ipsMarkedForRelease, ip)
				delete(n.ipv4Alloc.ipReleaseStatus, ip)
			}

			// If it's still released and not in the pool, we don't need to do anything
			continue
		}

		if status, ok := n.ipv4Alloc.ipReleaseStatus[ip]; ok && status != ipamOption.IPAMReleased {
			delete(n.ipv4Alloc.ipsMarkedForRelease, ip)
			delete(n.ipv4Alloc.ipReleaseStatus, ip)
		}
	}
}

// handleIPReleaseResponse handles IPs agent has already responded to
func (n *Node) handleIPReleaseResponse(markedIP string, ipsToRelease *[]string) bool {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	if n.resource.Status.IPAM.ReleaseIPs != nil {
		if status, ok := n.resource.Status.IPAM.ReleaseIPs[markedIP]; ok {
			switch status {
			case ipamOption.IPAMReadyForRelease:
				*ipsToRelease = append(*ipsToRelease, markedIP)
			case ipamOption.IPAMDoNotRelease:
				delete(n.ipv4Alloc.ipsMarkedForRelease, markedIP)
				delete(n.ipv4Alloc.ipReleaseStatus, markedIP)
			}
			// 'released' state is already handled in removeStaleReleaseIPs()
			// Other states don't need additional handling.
			return true
		}
	}
	return false
}

func (n *Node) deleteLocalReleaseStatus(ip string) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	delete(n.ipv4Alloc.ipReleaseStatus, ip)
}

// handleIPRelease implements IP release handshake needed for releasing excess IPs on the node.
// Operator initiates the handshake after an IP remains unused and excess for more than the number of seconds configured
// by excess-ip-release-delay flag. Operator uses a map in ciliumnode's IPAM status field to exchange handshake
// information with the agent. Once the operator marks an IP for release, agent can either acknowledge or NACK IPs.
// If agent acknowledges, operator will release the IP and update the state to released. After the IP is removed from
// spec.ipam.pool and status is set to released, agent will remove the entry from map completing the handshake.
// Handshake is implemented with 4 states :
// * marked-for-release : Set by operator as possible candidate for IP
// * ready-for-release  : Acknowledged as safe to release by agent
// * do-not-release     : IP already in use / not owned by the node. Set by agent
// * released           : IP successfully released. Set by operator
//
// Handshake would be aborted if there are new allocations and the node doesn't have IPs in excess anymore.
func (n *Node) handleIPRelease(ctx context.Context, a *maintenanceAction) (instanceMutated bool, err error) {
	var ipsToMark []string
	var ipsToRelease []string

	// Update timestamps for IPs from this iteration
	releaseTS := time.Now()
	if a.release != nil && a.release.IPsToRelease != nil {
		for _, ip := range a.release.IPsToRelease {
			if _, ok := n.ipv4Alloc.ipsMarkedForRelease[ip]; !ok {
				n.ipv4Alloc.ipsMarkedForRelease[ip] = releaseTS
			}
		}
	}

	if n.ipv4Alloc.ipsMarkedForRelease == nil || a.release == nil || len(a.release.IPsToRelease) == 0 {
		// Resetting ipsMarkedForRelease if there are no IPs to release in this iteration
		n.ipv4Alloc.ipsMarkedForRelease = make(map[string]time.Time)
	}

	for markedIP, ts := range n.ipv4Alloc.ipsMarkedForRelease {
		// Determine which IPs are still marked for release.
		stillMarkedForRelease := slices.Contains(a.release.IPsToRelease, markedIP)
		if !stillMarkedForRelease {
			// n.determineMaintenanceAction() only returns the IPs on the interface with maximum number of IPs that
			// can be freed up. If the selected interface changes or if this IP is not excess anymore, remove entry
			// from local maps.
			delete(n.ipv4Alloc.ipsMarkedForRelease, markedIP)
			n.deleteLocalReleaseStatus(markedIP)
			continue
		}
		// Check if the IP release waiting period elapsed
		if ts.Add(time.Duration(operatorOption.Config.ExcessIPReleaseDelay) * time.Second).After(time.Now()) {
			continue
		}
		// Handling for IPs we've already heard back from agent.
		if n.handleIPReleaseResponse(markedIP, &ipsToRelease) {
			continue
		}
		// markedIP can now be considered excess and is not currently in an active handshake
		ipsToMark = append(ipsToMark, markedIP)
	}

	n.mutex.Lock()
	for _, ip := range ipsToMark {
		n.logger.Load().Debug(
			"Marking IP for release",
			logfields.IPAddr, ip,
		)
		n.ipv4Alloc.ipReleaseStatus[ip] = ipamOption.IPAMMarkForRelease
	}
	n.mutex.Unlock()

	// Abort handshake for IPs that are in the middle of handshake, but are no longer considered excess
	var excessMap map[string]bool
	if a.release != nil && len(a.release.IPsToRelease) > 0 {
		excessMap = make(map[string]bool, len(a.release.IPsToRelease))
		for _, ip := range a.release.IPsToRelease {
			excessMap[ip] = true
		}
	}
	n.abortNoLongerExcessIPs(excessMap)

	if len(ipsToRelease) > 0 {
		a.release.IPsToRelease = ipsToRelease
		scopedLog := n.logger.Load().With(
			logfields.Available, n.stats.IPv4.AvailableIPs,
			logfields.Used, n.stats.IPv4.UsedIPs,
			logfields.Excess, n.stats.IPv4.ExcessIPs,
			logfields.ExcessIPs, a.release.IPsToRelease,
			logfields.Releasing, ipsToRelease,
			logfields.SelectedInterface, a.release.InterfaceID,
			logfields.SelectedPoolID, a.release.PoolID)
		start := time.Now()
		// Unassign unneeded IPPrefixes
		if len(a.release.IPPrefixesToRelease) > 0 {
			err := n.ops.ReleaseIPPrefixes(ctx, a.release)
			if err != nil {
				n.manager.metricsAPI.ReleaseAttempt(releaseIPPrefixes, failed, string(a.release.PoolID), metrics.SinceInSeconds(start))
				scopedLog.Warn(
					"Unable to unassign ipPrefixes from interface",
					logfields.Error, err,
					logfields.SelectedInterface, a.release.InterfaceID,
					logfields.ReleasingAddresses, a.release.IPPrefixesToRelease,
				)
				return false, err
			}
			n.manager.metricsAPI.ReleaseAttempt(releaseIPPrefixes, success, string(a.release.PoolID), metrics.SinceInSeconds(start))
			n.manager.metricsAPI.AddIPRelease(string(a.release.PoolID), int64(len(a.release.IPsToRelease)))
		}

		err := n.ops.ReleaseIPs(ctx, a.release)
		if err == nil {
			n.manager.metricsAPI.ReleaseAttempt(releaseIP, success, string(a.release.PoolID), metrics.SinceInSeconds(start))
			n.manager.metricsAPI.AddIPRelease(string(a.release.PoolID), int64(len(a.release.IPsToRelease)))
			// Remove the IPs from ipsMarkedForRelease
			n.mutex.Lock()
			for _, ip := range ipsToRelease {
				delete(n.ipv4Alloc.ipsMarkedForRelease, ip)
				n.ipv4Alloc.ipReleaseStatus[ip] = ipamOption.IPAMReleased
			}
			n.mutex.Unlock()
			return true, nil
		}
		n.manager.metricsAPI.ReleaseAttempt(releaseIP, failed, string(a.release.PoolID), metrics.SinceInSeconds(start))
		scopedLog.Warn(
			"Unable to unassign IPs from interface",
			logfields.Error, err,
			logfields.SelectedInterface, a.release.InterfaceID,
			logfields.ReleasingAddresses, len(a.release.IPsToRelease),
		)
		return false, err
	}
	return false, nil
}

// handleIPAllocation allocates the necessary IPs needed to resolve deficit on the node.
// If existing interfaces don't have enough capacity, new interface would be created.
func (n *Node) handleIPAllocation(ctx context.Context, a *maintenanceAction) (instanceMutated bool, err error) {
	if a.allocation == nil {
		n.logger.Load().Debug("No allocation action required")
		return false, nil
	}

	// Assign needed addresses
	if a.allocation.IPv4.AvailableForAllocation > 0 {
		a.allocation.IPv4.AvailableForAllocation = min(a.allocation.IPv4.AvailableForAllocation, a.allocation.IPv4.MaxIPsToAllocate)

		start := time.Now()
		err := n.ops.AllocateIPs(ctx, a.allocation)
		if err == nil {
			n.manager.metricsAPI.AllocationAttempt(allocateIP, success, string(a.allocation.PoolID), metrics.SinceInSeconds(start))
			n.manager.metricsAPI.AddIPAllocation(string(a.allocation.PoolID), int64(a.allocation.IPv4.AvailableForAllocation))
			return true, nil
		}

		n.manager.metricsAPI.AllocationAttempt(allocateIP, failed, string(a.allocation.PoolID), metrics.SinceInSeconds(start))
		n.logger.Load().Warn(
			"Unable to assign additional IPs to interface, will create new interface",
			logfields.Error, err,
			logfields.SelectedInterface, a.allocation.InterfaceID,
			logfields.IPsToAllocate, a.allocation.IPv4.AvailableForAllocation,
		)
	}

	return n.createInterface(ctx, a.allocation)
}

// maintainIPPool attempts to allocate or release all required IPs to fulfill the needed gap.
// returns instanceMutated which tracks if state changed with the cloud provider and is used
// to determine if IPAM pool maintainer trigger func needs to be invoked.
func (n *Node) maintainIPPool(ctx context.Context) (instanceMutated bool, err error) {
	if n.manager.releaseExcessIPs {
		n.removeStaleReleaseIPs()
	}

	if len(n.getStaticIPTags()) > 0 {
		if n.stats.IPv4.AssignedStaticIP == "" {
			ip, err := n.ops.AllocateStaticIP(ctx, n.getStaticIPTags())
			if err != nil {
				return false, err
			}
			n.stats.IPv4.AssignedStaticIP = ip
		}
	}

	a, err := n.determineMaintenanceAction()
	if err != nil {
		n.abortNoLongerExcessIPs(nil)
		return false, err
	}

	// Maintenance request has already been fulfilled
	if a == nil {
		n.abortNoLongerExcessIPs(nil)
		return false, nil
	}

	if instanceMutated, err := n.handleIPRelease(ctx, a); instanceMutated || err != nil {
		return instanceMutated, err
	}

	return n.handleIPAllocation(ctx, a)
}

func (n *Node) isInstanceRunning() (isRunning bool) {
	n.mutex.RLock()
	isRunning = n.instanceRunning
	n.mutex.RUnlock()
	return
}

func (n *Node) requireResync() {
	n.mutex.Lock()
	n.resyncNeeded = time.Now()
	n.mutex.Unlock()
}

func (n *Node) updateLastResync(syncTime time.Time) {
	n.mutex.Lock()
	if syncTime.After(n.resyncNeeded) {
		n.logger.Load().Debug("Resetting resyncNeeded")
		n.resyncNeeded = time.Time{}
	}
	n.mutex.Unlock()
}

// MaintainIPPool attempts to allocate or release all required IPs to fulfill
// the needed gap. If required, interfaces are created.
func (n *Node) MaintainIPPool(ctx context.Context) error {
	// As long as the instances API is unstable, don't perform any
	// operation that can mutate state.
	if !n.manager.InstancesAPIIsReady() {
		if n.retry != nil {
			n.retry.Trigger()
		}
		return fmt.Errorf("instances API is unstable. Blocking mutating operations. See logs for details.")
	}

	// If the instance has stopped running for less than a minute, don't attempt any deficit
	// resolution and wait for the custom resource to be updated as a sign
	// of life.
	if !n.isInstanceRunning() && n.instanceStoppedRunning.Add(time.Minute).After(time.Now()) {
		return nil
	}

	instanceMutated, err := n.maintainIPPool(ctx)
	if err == nil {
		n.logger.Load().Debug("Setting resync needed")
		n.requireResync()
	}
	n.poolMaintenanceComplete()
	n.recalculate(ctx)
	if instanceMutated || err != nil {
		n.instanceSync.Trigger()
	}
	return err
}

// PopulateIPReleaseStatus Updates cilium node IPAM status with excess IP release data
func (n *Node) PopulateIPReleaseStatus(node *v2.CiliumNode) {
	// maintainIPPool() might not have run yet since the last update from agent.
	// Attempt to remove any stale entries
	n.removeStaleReleaseIPs()
	n.mutex.Lock()
	defer n.mutex.Unlock()
	releaseStatus := make(map[string]ipamTypes.IPReleaseStatus)
	for ip, status := range n.ipv4Alloc.ipReleaseStatus {
		if existingStatus, ok := node.Status.IPAM.ReleaseIPs[ip]; ok && status == ipamOption.IPAMMarkForRelease {
			// retain status if agent already responded to this IP
			if existingStatus == ipamOption.IPAMReadyForRelease || existingStatus == ipamOption.IPAMDoNotRelease {
				releaseStatus[ip] = existingStatus
				continue
			}
		}
		releaseStatus[ip] = ipamTypes.IPReleaseStatus(status)
	}
	node.Status.IPAM.ReleaseIPs = releaseStatus
}

func (n *Node) PopulateStaticIPStatus(node *v2.CiliumNode) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	if n.stats.IPv4.AssignedStaticIP != "" {
		node.Status.IPAM.AssignedStaticIP = n.stats.IPv4.AssignedStaticIP
	}
}

// syncToAPIServer synchronizes the contents of the CiliumNode resource
// [(*Node).resource)] with the K8s apiserver. This operation occurs on an
// interval to refresh the CiliumNode resource.
//
// For Azure and ENI IPAM modes, this function serves two purposes: (1)
// finalizes the initialization of the CiliumNode resource (setting
// PreAllocate) and (2) to keep the resource up-to-date with K8s.
//
// To initialize, or seed, the CiliumNode resource, the PreAllocate field is
// populated with a default value and then is adjusted as necessary.
func (n *Node) syncToAPIServer() error {
	n.logger.Load().Debug("Refreshing node")

	node := n.ResourceCopy()
	// n.resource may not have been assigned yet
	if node == nil {
		return nil
	}

	origNode := node.DeepCopy()

	// We create a snapshot of the IP pool before we update the status. This
	// ensures that the pool in the spec is always older than the IPAM
	// information in the status.
	// This ordering is important, because otherwise a new IP could be added to
	// the pool after we updated the status, thereby creating a situation where
	// the agent does not have the necessary IPAM information to use the newly
	// added IP.
	// When an IP is removed, this is also safe. IP release is done via
	// handshake, where the agent will never use any IP where it has
	// acknowledged the release handshake. Therefore, having an already
	// released IP in the pool is fine, as the agent will ignore it.
	pool := n.Pool()

	// Always update the status first to ensure that the IPAM information
	// is synced for all addresses that are marked as available.
	//
	// Two attempts are made in case the local resource is outdated. If the
	// second attempt fails as well we are likely under heavy contention,
	// fall back to the controller based background interval to retry.
	maxRetries := 2
	for retry := range maxRetries {
		if node.Status.IPAM.Used == nil {
			node.Status.IPAM.Used = ipamTypes.AllocationMap{}
		}

		n.ops.PopulateStatusFields(node)
		n.PopulateIPReleaseStatus(node)
		n.PopulateStaticIPStatus(node)

		err := n.update(origNode, node, true)
		if err == nil {
			break
		} else if retry+1 < maxRetries {
			n.logger.Load().Info("Failed to update CiliumNode status, will retry", logfields.Error, err)
		} else {
			n.logger.Load().Warn("Unable to update CiliumNode status", logfields.Error, err)
			return err
		}
	}

	for retry := range maxRetries {
		node.Spec.IPAM.Pool = pool
		n.logger.Load().Debug("Updating node in apiserver", logfields.PoolSize, len(node.Spec.IPAM.Pool))

		// The PreAllocate value is added here rather than where the CiliumNode
		// resource is created ((*NodeDiscovery).mutateNodeResource() inside
		// pkg/nodediscovery), because mutateNodeResource() does not have
		// access to the ipam.Node object. Since we are in the CiliumNode
		// update sync loop, we can compute the value.
		if node.Spec.IPAM.PreAllocate == 0 {
			node.Spec.IPAM.PreAllocate = n.ops.GetMinimumAllocatableIPv4()
		}

		err := n.update(origNode, node, false)
		if err == nil {
			break
		} else if retry+1 < maxRetries {
			n.logger.Load().Info("Failed to update CiliumNode spec, will retry", logfields.Error, err)
		} else {
			n.logger.Load().Warn("Unable to update CiliumNode spec", logfields.Error, err)
			return err
		}
	}

	n.logger.Load().Debug("Node refreshed")

	return nil
}

// update is a helper function for syncToAPIServer(). This function updates the
// CiliumNode resource spec or status depending on `status`. The resource is
// updated from `origNode` to `node`.
//
// Note that the `origNode` and `node` pointers will have their underlying
// values modified in this function! The following is an outline of when
// `origNode` and `node` pointers are updated:
//   - `node` is updated when we succeed in updating to update the resource to
//     the apiserver.
//   - `origNode` and `node` are updated when we fail to update the resource,
//     but we succeed in retrieving the latest version of it from the
//     apiserver.
func (n *Node) update(origNode, node *v2.CiliumNode, status bool) error {
	var (
		updatedNode    *v2.CiliumNode
		updateErr, err error
	)

	if status {
		updatedNode, updateErr = n.manager.k8sAPI.UpdateStatus(origNode, node)
	} else {
		updatedNode, updateErr = n.manager.k8sAPI.Update(origNode, node)
	}

	if updatedNode != nil && updatedNode.Name != "" {
		*node = *updatedNode
		if updateErr == nil {
			return nil
		}
	} else if updateErr != nil {
		var newNode *v2.CiliumNode
		newNode, err = n.manager.k8sAPI.Get(node.Name)
		if err != nil {
			return err
		}

		// Propagate the error in the case that we are on our last attempt and
		// we never succeeded in updating the resource.
		//
		// Also, propagate the reference to the nodes in the case we've
		// succeeded in updating the CiliumNode status. The reason is because
		// the subsequent run will be to update the CiliumNode spec and we need
		// to ensure we have the most up-to-date CiliumNode references before
		// doing that operation, hence the deep copies.
		err = updateErr
		*node = *newNode
		*origNode = *node
	} else /* updateErr == nil */ {
		err = updateErr
	}

	return err
}
