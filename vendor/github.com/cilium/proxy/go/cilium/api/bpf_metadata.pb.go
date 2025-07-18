// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.6
// 	protoc        v5.29.3
// source: cilium/api/bpf_metadata.proto

package cilium

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	durationpb "google.golang.org/protobuf/types/known/durationpb"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type BpfMetadata struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// File system root for bpf. Bpf will not be used if left empty.
	BpfRoot string `protobuf:"bytes,1,opt,name=bpf_root,json=bpfRoot,proto3" json:"bpf_root,omitempty"`
	// 'true' if the filter is on ingress listener, 'false' for egress listener.
	IsIngress bool `protobuf:"varint,2,opt,name=is_ingress,json=isIngress,proto3" json:"is_ingress,omitempty"`
	// Use of the original source address requires kernel datapath support which
	// may or may not be available. 'true' if original source address
	// should be used. Original source address use may still be
	// skipped in scenarios where it is knows to not work.
	UseOriginalSourceAddress bool `protobuf:"varint,3,opt,name=use_original_source_address,json=useOriginalSourceAddress,proto3" json:"use_original_source_address,omitempty"`
	// True if the listener is used for an L7 LB. In this case policy enforcement is done on the
	// destination selected by the listener rather than on the original destination address. For
	// local sources the source endpoint ID is set in socket mark instead of source security ID if
	// 'use_original_source_address' is also true, so that the local source's egress policy is
	// enforced on the bpf datapath.
	// Only valid for egress.
	IsL7Lb bool `protobuf:"varint,4,opt,name=is_l7lb,json=isL7lb,proto3" json:"is_l7lb,omitempty"`
	// Source address to be used whenever the original source address is not used.
	// Either ipv4_source_address or ipv6_source_address depending on the address
	// family of the destination address. If left empty, and no Envoy Cluster Bind
	// Config is provided, the source address will be picked by the local IP stack.
	Ipv4SourceAddress string `protobuf:"bytes,5,opt,name=ipv4_source_address,json=ipv4SourceAddress,proto3" json:"ipv4_source_address,omitempty"`
	Ipv6SourceAddress string `protobuf:"bytes,6,opt,name=ipv6_source_address,json=ipv6SourceAddress,proto3" json:"ipv6_source_address,omitempty"`
	// True if policy should be enforced on l7 LB used. The policy bound to the configured
	// ipv[46]_source_addresses, which must be explicitly set, applies. Ingress policy is
	// enforced on the security identity of the original (e.g., external) source. Egress
	// policy is enforced on the security identity of the backend selected by the load balancer.
	//
	// Deprecation note: This option will be forced 'true' and deprecated when Cilium 1.15 is
	// the oldest supported release.
	EnforcePolicyOnL7Lb bool `protobuf:"varint,7,opt,name=enforce_policy_on_l7lb,json=enforcePolicyOnL7lb,proto3" json:"enforce_policy_on_l7lb,omitempty"`
	// proxy_id is passed to access log messages and allows relating access log messages to
	// listeners.
	ProxyId uint32 `protobuf:"varint,8,opt,name=proxy_id,json=proxyId,proto3" json:"proxy_id,omitempty"`
	// policy_update_warning_limit is the time in milliseconds after which a warning is logged if
	// network policy update took longer
	// Deprecated, has no effect.
	PolicyUpdateWarningLimit *durationpb.Duration `protobuf:"bytes,9,opt,name=policy_update_warning_limit,json=policyUpdateWarningLimit,proto3" json:"policy_update_warning_limit,omitempty"`
	// l7lb_policy_name is the name of the L7LB policy that is enforced on the listener.
	// This is optional field.
	L7LbPolicyName string `protobuf:"bytes,10,opt,name=l7lb_policy_name,json=l7lbPolicyName,proto3" json:"l7lb_policy_name,omitempty"`
	// original_source_so_linger_time specifies the number of seconds to linger on socket close.
	// Only used if use_original_source_address is also true, and the original source address
	// is used in the upstream connections. Value 0 causes connections to be reset on close (TCP RST).
	// Values above 0 cause the Envoy worker thread to block up to the given number of seconds while
	// the connection is closing. If the timeout is reached the connection is being reset (TCP RST).
	// This option may be needed for allowing new connections to successfully bind to the original
	// source address and port.
	OriginalSourceSoLingerTime *uint32 `protobuf:"varint,11,opt,name=original_source_so_linger_time,json=originalSourceSoLingerTime,proto3,oneof" json:"original_source_so_linger_time,omitempty"`
	// Name of the pin file for opening bpf ipcache in "<bpf_root>/tc/globals/". If empty, defaults to
	// "cilium_ipcache" for backwards compatibility.
	// Only used if 'bpf_root' is non-empty and 'use_nphds' is 'false'.
	IpcacheName string `protobuf:"bytes,12,opt,name=ipcache_name,json=ipcacheName,proto3" json:"ipcache_name,omitempty"`
	// Use Network Policy Hosts xDS (NPHDS) protocol to sync IP/ID mappings.
	// Network Policy xDS (NPDS) will only be used if this is 'true' or 'bpf_root' is non-empty.
	// If 'use_nphds' is 'false' ipcache named by 'ipcache_name' is used instead.
	UseNphds bool `protobuf:"varint,13,opt,name=use_nphds,json=useNphds,proto3" json:"use_nphds,omitempty"`
	// Duration to reuse ipcache results until the entry is looked up from bpf ipcache again.
	// Defaults to 3 milliseconds.
	CacheEntryTtl *durationpb.Duration `protobuf:"bytes,14,opt,name=cache_entry_ttl,json=cacheEntryTtl,proto3" json:"cache_entry_ttl,omitempty"`
	// Cache is garbage collected at interval 10 times the ttl (default 30 ms).
	CacheGcInterval *durationpb.Duration `protobuf:"bytes,15,opt,name=cache_gc_interval,json=cacheGcInterval,proto3" json:"cache_gc_interval,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *BpfMetadata) Reset() {
	*x = BpfMetadata{}
	mi := &file_cilium_api_bpf_metadata_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *BpfMetadata) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*BpfMetadata) ProtoMessage() {}

func (x *BpfMetadata) ProtoReflect() protoreflect.Message {
	mi := &file_cilium_api_bpf_metadata_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use BpfMetadata.ProtoReflect.Descriptor instead.
func (*BpfMetadata) Descriptor() ([]byte, []int) {
	return file_cilium_api_bpf_metadata_proto_rawDescGZIP(), []int{0}
}

func (x *BpfMetadata) GetBpfRoot() string {
	if x != nil {
		return x.BpfRoot
	}
	return ""
}

func (x *BpfMetadata) GetIsIngress() bool {
	if x != nil {
		return x.IsIngress
	}
	return false
}

func (x *BpfMetadata) GetUseOriginalSourceAddress() bool {
	if x != nil {
		return x.UseOriginalSourceAddress
	}
	return false
}

func (x *BpfMetadata) GetIsL7Lb() bool {
	if x != nil {
		return x.IsL7Lb
	}
	return false
}

func (x *BpfMetadata) GetIpv4SourceAddress() string {
	if x != nil {
		return x.Ipv4SourceAddress
	}
	return ""
}

func (x *BpfMetadata) GetIpv6SourceAddress() string {
	if x != nil {
		return x.Ipv6SourceAddress
	}
	return ""
}

func (x *BpfMetadata) GetEnforcePolicyOnL7Lb() bool {
	if x != nil {
		return x.EnforcePolicyOnL7Lb
	}
	return false
}

func (x *BpfMetadata) GetProxyId() uint32 {
	if x != nil {
		return x.ProxyId
	}
	return 0
}

func (x *BpfMetadata) GetPolicyUpdateWarningLimit() *durationpb.Duration {
	if x != nil {
		return x.PolicyUpdateWarningLimit
	}
	return nil
}

func (x *BpfMetadata) GetL7LbPolicyName() string {
	if x != nil {
		return x.L7LbPolicyName
	}
	return ""
}

func (x *BpfMetadata) GetOriginalSourceSoLingerTime() uint32 {
	if x != nil && x.OriginalSourceSoLingerTime != nil {
		return *x.OriginalSourceSoLingerTime
	}
	return 0
}

func (x *BpfMetadata) GetIpcacheName() string {
	if x != nil {
		return x.IpcacheName
	}
	return ""
}

func (x *BpfMetadata) GetUseNphds() bool {
	if x != nil {
		return x.UseNphds
	}
	return false
}

func (x *BpfMetadata) GetCacheEntryTtl() *durationpb.Duration {
	if x != nil {
		return x.CacheEntryTtl
	}
	return nil
}

func (x *BpfMetadata) GetCacheGcInterval() *durationpb.Duration {
	if x != nil {
		return x.CacheGcInterval
	}
	return nil
}

var File_cilium_api_bpf_metadata_proto protoreflect.FileDescriptor

const file_cilium_api_bpf_metadata_proto_rawDesc = "" +
	"\n" +
	"\x1dcilium/api/bpf_metadata.proto\x12\x06cilium\x1a\x1egoogle/protobuf/duration.proto\"\x89\x06\n" +
	"\vBpfMetadata\x12\x19\n" +
	"\bbpf_root\x18\x01 \x01(\tR\abpfRoot\x12\x1d\n" +
	"\n" +
	"is_ingress\x18\x02 \x01(\bR\tisIngress\x12=\n" +
	"\x1buse_original_source_address\x18\x03 \x01(\bR\x18useOriginalSourceAddress\x12\x17\n" +
	"\ais_l7lb\x18\x04 \x01(\bR\x06isL7lb\x12.\n" +
	"\x13ipv4_source_address\x18\x05 \x01(\tR\x11ipv4SourceAddress\x12.\n" +
	"\x13ipv6_source_address\x18\x06 \x01(\tR\x11ipv6SourceAddress\x123\n" +
	"\x16enforce_policy_on_l7lb\x18\a \x01(\bR\x13enforcePolicyOnL7lb\x12\x19\n" +
	"\bproxy_id\x18\b \x01(\rR\aproxyId\x12X\n" +
	"\x1bpolicy_update_warning_limit\x18\t \x01(\v2\x19.google.protobuf.DurationR\x18policyUpdateWarningLimit\x12(\n" +
	"\x10l7lb_policy_name\x18\n" +
	" \x01(\tR\x0el7lbPolicyName\x12G\n" +
	"\x1eoriginal_source_so_linger_time\x18\v \x01(\rH\x00R\x1aoriginalSourceSoLingerTime\x88\x01\x01\x12!\n" +
	"\fipcache_name\x18\f \x01(\tR\vipcacheName\x12\x1b\n" +
	"\tuse_nphds\x18\r \x01(\bR\buseNphds\x12A\n" +
	"\x0fcache_entry_ttl\x18\x0e \x01(\v2\x19.google.protobuf.DurationR\rcacheEntryTtl\x12E\n" +
	"\x11cache_gc_interval\x18\x0f \x01(\v2\x19.google.protobuf.DurationR\x0fcacheGcIntervalB!\n" +
	"\x1f_original_source_so_linger_timeB.Z,github.com/cilium/proxy/go/cilium/api;ciliumb\x06proto3"

var (
	file_cilium_api_bpf_metadata_proto_rawDescOnce sync.Once
	file_cilium_api_bpf_metadata_proto_rawDescData []byte
)

func file_cilium_api_bpf_metadata_proto_rawDescGZIP() []byte {
	file_cilium_api_bpf_metadata_proto_rawDescOnce.Do(func() {
		file_cilium_api_bpf_metadata_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_cilium_api_bpf_metadata_proto_rawDesc), len(file_cilium_api_bpf_metadata_proto_rawDesc)))
	})
	return file_cilium_api_bpf_metadata_proto_rawDescData
}

var file_cilium_api_bpf_metadata_proto_msgTypes = make([]protoimpl.MessageInfo, 1)
var file_cilium_api_bpf_metadata_proto_goTypes = []any{
	(*BpfMetadata)(nil),         // 0: cilium.BpfMetadata
	(*durationpb.Duration)(nil), // 1: google.protobuf.Duration
}
var file_cilium_api_bpf_metadata_proto_depIdxs = []int32{
	1, // 0: cilium.BpfMetadata.policy_update_warning_limit:type_name -> google.protobuf.Duration
	1, // 1: cilium.BpfMetadata.cache_entry_ttl:type_name -> google.protobuf.Duration
	1, // 2: cilium.BpfMetadata.cache_gc_interval:type_name -> google.protobuf.Duration
	3, // [3:3] is the sub-list for method output_type
	3, // [3:3] is the sub-list for method input_type
	3, // [3:3] is the sub-list for extension type_name
	3, // [3:3] is the sub-list for extension extendee
	0, // [0:3] is the sub-list for field type_name
}

func init() { file_cilium_api_bpf_metadata_proto_init() }
func file_cilium_api_bpf_metadata_proto_init() {
	if File_cilium_api_bpf_metadata_proto != nil {
		return
	}
	file_cilium_api_bpf_metadata_proto_msgTypes[0].OneofWrappers = []any{}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_cilium_api_bpf_metadata_proto_rawDesc), len(file_cilium_api_bpf_metadata_proto_rawDesc)),
			NumEnums:      0,
			NumMessages:   1,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_cilium_api_bpf_metadata_proto_goTypes,
		DependencyIndexes: file_cilium_api_bpf_metadata_proto_depIdxs,
		MessageInfos:      file_cilium_api_bpf_metadata_proto_msgTypes,
	}.Build()
	File_cilium_api_bpf_metadata_proto = out.File
	file_cilium_api_bpf_metadata_proto_goTypes = nil
	file_cilium_api_bpf_metadata_proto_depIdxs = nil
}
