// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package monitor

import (
	"fmt"
	"net"

	"github.com/cilium/cilium/pkg/byteorder"
	"github.com/cilium/cilium/pkg/monitor/api"
	"github.com/cilium/cilium/pkg/types"
)

// Service translation event point in socket trace event messages
const (
	XlatePointUnknown = iota
	XlatePointPreDirectionFwd
	XlatePointPostDirectionFwd
	XlatePointPreDirectionRev
	XlatePointPostDirectionRev
)

// L4 protocol for socket trace event messages
const (
	L4ProtocolUnknown = iota
	L4ProtocolTCP
	L4ProtocolUDP
)

const TraceSockNotifyFlagIPv6 uint8 = 0x1

const (
	TraceSockNotifyLen = 38
)

// TraceSockNotify is message format for socket trace notifications sent from datapath.
// Keep this in sync to the datapath structure (trace_sock_notify) defined in
// bpf/lib/trace_sock.h
type TraceSockNotify struct {
	api.DefaultSrcDstGetter

	Type       uint8
	XlatePoint uint8
	DstIP      types.IPv6
	DstPort    uint16
	SockCookie uint64
	CgroupId   uint64
	L4Proto    uint8
	Flags      uint8
}

// Dump prints the message according to the verbosity level specified
func (t *TraceSockNotify) Dump(args *api.DumpArgs) {
	// Currently only printed with the debug option. Extend it to info and json.
	// GH issue: https://github.com/cilium/cilium/issues/21510
	if args.Verbosity == api.DEBUG {
		fmt.Fprintf(args.Buf, "%s [%s] cgroup_id: %d sock_cookie: %d, dst [%s]:%d %s \n",
			args.CpuPrefix, t.XlatePointStr(), t.CgroupId, t.SockCookie, t.IP(), t.DstPort, t.L4ProtoStr())
	}
}

// Decode decodes the message in 'data' into the struct.
func (t *TraceSockNotify) Decode(data []byte) error {
	if l := len(data); l < TraceSockNotifyLen {
		return fmt.Errorf("unexpected TraceSockNotify data length, expected %d but got %d", TraceSockNotifyLen, l)
	}

	t.Type = data[0]
	t.XlatePoint = data[1]
	copy(t.DstIP[:], data[2:18])
	t.DstPort = byteorder.Native.Uint16(data[18:20])
	t.SockCookie = byteorder.Native.Uint64(data[20:28])
	t.CgroupId = byteorder.Native.Uint64(data[28:36])
	t.L4Proto = data[36]
	t.Flags = data[37]

	return nil
}

func (t *TraceSockNotify) XlatePointStr() string {
	switch t.XlatePoint {
	case XlatePointPreDirectionFwd:
		return "pre-xlate-fwd"
	case XlatePointPostDirectionFwd:
		return "post-xlate-fwd"
	case XlatePointPreDirectionRev:
		return "pre-xlate-rev"
	case XlatePointPostDirectionRev:
		return "post-xlate-rev"
	default:
		return "unknown"
	}
}

// IP returns the IPv4 or IPv6 address field.
func (t *TraceSockNotify) IP() net.IP {
	if (t.Flags & TraceSockNotifyFlagIPv6) != 0 {
		return t.DstIP[:]
	}
	return t.DstIP[:4]
}

func (t *TraceSockNotify) L4ProtoStr() string {
	switch t.L4Proto {
	case L4ProtocolTCP:
		return "tcp"
	case L4ProtocolUDP:
		return "udp"
	default:
		return "unknown"
	}
}
