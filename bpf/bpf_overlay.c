// SPDX-License-Identifier: (GPL-2.0-only OR BSD-2-Clause)
/* Copyright Authors of Cilium */

#include <bpf/ctx/skb.h>
#include <bpf/api.h>

#include <bpf/config/node.h>
#include <bpf/config/global.h>
#include <netdev_config.h>
#include "lib/mcast.h"

#define IS_BPF_OVERLAY 1

/* WORLD_IPV{4,6}_ID varies based on dualstack being enabled. Real values are
 * written into node_config.h at runtime. */
#define SECLABEL WORLD_ID
#define SECLABEL_IPV4 WORLD_IPV4_ID
#define SECLABEL_IPV6 WORLD_IPV6_ID

/* Controls the inclusion of the CILIUM_CALL_HANDLE_ICMP6_NS section in the
 * bpf_lxc object file.
 */
#define SKIP_ICMPV6_NS_HANDLING

/* Controls the inclusion of the CILIUM_CALL_SRV6 section in the object file.
 */
#define SKIP_SRV6_HANDLING

#include "lib/tailcall.h"
#include "lib/common.h"
#include "lib/edt.h"
#include "lib/encrypt.h"
#include "lib/eps.h"
#include "lib/ipv6.h"
#include "lib/eth.h"
#include "lib/dbg.h"
#include "lib/trace.h"
#include "lib/l3.h"
#include "lib/local_delivery.h"
#include "lib/drop.h"
#include "lib/identity.h"
#include "lib/node.h"
#include "lib/nodeport.h"
#include "lib/nodeport_egress.h"
#include "lib/clustermesh.h"
#include "lib/egress_gateway.h"

#ifdef ENABLE_VTEP
#include "lib/vtep.h"
#include "lib/arp.h"
#include "lib/encap.h"
#endif /* ENABLE_VTEP */

#define overlay_ingress_policy_hook(ctx, ip4, identity, ext_err) CTX_ACT_OK

#ifdef ENABLE_IPV6
static __always_inline int handle_ipv6(struct __ctx_buff *ctx,
				       __u32 *identity,
				       __s8 *ext_err __maybe_unused)
{
	int ret, l3_off = ETH_HLEN;
	struct remote_endpoint_info *info;
	void *data_end, *data;
	struct ipv6hdr *ip6;
	struct endpoint_info *ep;
	bool decrypted = false;
	bool __maybe_unused is_dsr = false;
	fraginfo_t fraginfo __maybe_unused;

	/* verifier workaround (dereference of modified ctx ptr) */
	if (!revalidate_data_pull(ctx, &data, &data_end, &ip6))
		return DROP_INVALID;

#ifndef ENABLE_IPV6_FRAGMENTS
	fraginfo = ipv6_get_fraginfo(ctx, ip6);
	if (fraginfo < 0)
		return (int)fraginfo;
	if (ipfrag_is_fragment(fraginfo))
		return DROP_FRAG_NOSUPPORT;
#endif

#ifdef ENABLE_NODEPORT
	if (!ctx_skip_nodeport(ctx)) {
		bool punt_to_stack = false;

		ret = nodeport_lb6(ctx, ip6, *identity, &punt_to_stack,
				   ext_err, &is_dsr);
		/* nodeport_lb6() returns with TC_ACT_REDIRECT for
		 * traffic to L7 LB. Policy enforcement needs to take
		 * place after L7 LB has processed the packet, so we
		 * return to stack immediately here with
		 * TC_ACT_REDIRECT.
		 */
		if (ret < 0 || ret == TC_ACT_REDIRECT)
			return ret;
		if (punt_to_stack)
			return ret;
	}
#endif

	if (!revalidate_data(ctx, &data, &data_end, &ip6))
		return DROP_INVALID;

	/* Lookup the source in the ipcache. Before decryption this will be the
	 * outer source IP to get the source node ID. After decryption this
	 * will be the inner source IP to get the source security identity.
	 */
	info = lookup_ip6_remote_endpoint((union v6addr *)&ip6->saddr, 0);

#ifdef ENABLE_IPSEC
	decrypted = ((ctx->mark & MARK_MAGIC_HOST_MASK) == MARK_MAGIC_DECRYPT);
#endif
	if (decrypted) {
		if (info)
			*identity = info->sec_identity;

		cilium_dbg(ctx, info ? DBG_IP_ID_MAP_SUCCEED6 : DBG_IP_ID_MAP_FAILED6,
			   ((__u32 *)&ip6->saddr)[3], *identity);
	} else {
		/* Maybe overwrite the REMOTE_NODE_ID with
		 * KUBE_APISERVER_NODE_ID to support upgrade. After v1.12,
		 * identity_is_remote_node() should be removed.
		 *
		 * A packet that has DSR info and comes from `world` may have specific identity when
		 * a CNP that is using CIDR rules is applied.
		 */
		if (info && (identity_is_remote_node(*identity) ||
			     (is_dsr && identity_is_world_ipv6(*identity))))
			*identity = info->sec_identity;
	}

#ifdef ENABLE_IPSEC
	if (!decrypted) {
		__u16 node_id;

		/* IPSec is not currently enforce (feature coming soon)
		 * so for now just handle normally
		 */
		if (ip6->nexthdr != IPPROTO_ESP) {
			update_metrics(ctx_full_len(ctx), METRIC_INGRESS,
				       REASON_PLAINTEXT);
			goto not_esp;
		}

		node_id = lookup_ip6_node_id((union v6addr *)&ip6->saddr);
		if (!node_id)
			return DROP_NO_NODE_ID;
		set_ipsec_decrypt_mark(ctx, node_id);

		/* To IPSec stack on cilium_vxlan we are going to pass
		 * this up the stack but eth_type_trans has already labeled
		 * this as an OTHERHOST type packet. To avoid being dropped
		 * by IP stack before IPSec can be processed mark as a HOST
		 * packet.
		 */
		ctx_change_type(ctx, PACKET_HOST);

		send_trace_notify(ctx, TRACE_TO_STACK, *identity, UNKNOWN_ID,
				  TRACE_EP_ID_UNKNOWN, ctx->ingress_ifindex,
				  TRACE_REASON_ENCRYPTED, 0, bpf_htons(ETH_P_IPV6));

		return CTX_ACT_OK;
	}
	ctx->mark = 0;
not_esp:
#endif

#if defined(ENABLE_EGRESS_GATEWAY_COMMON)
	{
		__u32 egress_ifindex = 0;
		union v6addr snat_addr, daddr;

		ipv6_addr_copy(&daddr, (union v6addr *)&ip6->daddr);
		if (egress_gw_snat_needed_hook_v6((union v6addr *)&ip6->saddr,
						  &daddr, &snat_addr,
						  &egress_ifindex)) {
			if (ipv6_addr_equals(&snat_addr, &EGRESS_GATEWAY_NO_EGRESS_IP_V6))
				return DROP_NO_EGRESS_IP;

			ret = ipv6_l3(ctx, ETH_HLEN, NULL, NULL, METRIC_INGRESS);
			if (unlikely(ret != CTX_ACT_OK))
				return ret;

			set_identity_mark(ctx, *identity, MARK_MAGIC_EGW_DONE);

			/* to-netdev@bpf_host handles SNAT, so no need to do it here. */
			return egress_gw_fib_lookup_and_redirect_v6(ctx, &snat_addr,
								    &daddr, egress_ifindex,
								    ext_err);
		}
	}
#endif /* ENABLE_EGRESS_GATEWAY_COMMON */

#if defined(ENABLE_DSR) && (DSR_ENCAP_MODE == DSR_ENCAP_GENEVE)
	/* Pass incoming packets which will be returned using Geneve DSR
	 * to host-stack for conntrack entry insertion.
	 * Geneve DSR reply packets are processed by the host-stack,
	 * so this logic is needed to prevent the packets from being handled
	 * by netfilter in an unintended way.
	 */
	if (!is_defined(ENABLE_HOST_ROUTING) && is_dsr) {
		ctx_change_type(ctx, PACKET_HOST);
		return CTX_ACT_OK;
	}
#endif

	/* Deliver to local (non-host) endpoint: */
	ep = lookup_ip6_endpoint(ip6);
	if (ep && !(ep->flags & ENDPOINT_MASK_HOST_DELIVERY))
		return ipv6_local_delivery(ctx, l3_off, *identity, MARK_MAGIC_IDENTITY,
					   ep, METRIC_INGRESS, false, true);

	/* A packet entering the node from the tunnel and not going to a local
	 * endpoint has to be going to the local host.
	 */
	if (1) {
		union macaddr host_mac = CILIUM_HOST_MAC;
		union macaddr router_mac = THIS_INTERFACE_MAC;

		ret = ipv6_l3(ctx, ETH_HLEN, (__u8 *)&router_mac.addr,
			      (__u8 *)&host_mac.addr, METRIC_INGRESS);
		if (ret != CTX_ACT_OK)
			return ret;

		cilium_dbg_capture(ctx, DBG_CAPTURE_DELIVERY, CILIUM_NET_IFINDEX);
		return ctx_redirect(ctx, CILIUM_NET_IFINDEX, 0);
	}
}

__declare_tail(CILIUM_CALL_IPV6_FROM_OVERLAY)
int tail_handle_ipv6(struct __ctx_buff *ctx)
{
	__u32 src_sec_identity = ctx_load_and_clear_meta(ctx, CB_SRC_LABEL);
	__s8 ext_err = 0;
	int ret;

	ret = handle_ipv6(ctx, &src_sec_identity, &ext_err);

	if (IS_ERR(ret))
		return send_drop_notify_error_ext(ctx, src_sec_identity, ret, ext_err,
						  METRIC_INGRESS);
	return ret;
}
#endif /* ENABLE_IPV6 */

#ifdef ENABLE_IPV4
static __always_inline int ipv4_host_delivery(struct __ctx_buff *ctx, struct iphdr *ip4)
{
	if (1) {
		union macaddr host_mac = CILIUM_HOST_MAC;
		union macaddr router_mac = THIS_INTERFACE_MAC;
		int ret;

		ret = ipv4_l3(ctx, ETH_HLEN, (__u8 *)&router_mac.addr,
			      (__u8 *)&host_mac.addr, ip4);
		if (ret != CTX_ACT_OK)
			return ret;

		cilium_dbg_capture(ctx, DBG_CAPTURE_DELIVERY, CILIUM_NET_IFINDEX);
		return ctx_redirect(ctx, CILIUM_NET_IFINDEX, 0);
	}
}

#if defined(ENABLE_CLUSTER_AWARE_ADDRESSING) && defined(ENABLE_INTER_CLUSTER_SNAT)
static __always_inline int handle_inter_cluster_revsnat(struct __ctx_buff *ctx,
							__u32 src_sec_identity,
							__s8 *ext_err)
{
	int ret;
	struct iphdr *ip4;
	__u32 cluster_id = 0;
	void *data_end, *data;
	struct endpoint_info *ep;
	__u32 cluster_id_from_identity =
		extract_cluster_id_from_identity(src_sec_identity);
	const struct ipv4_nat_target target = {
	       .min_port = NODEPORT_PORT_MIN_NAT,
	       .max_port = NODEPORT_PORT_MAX_NAT,
	       .cluster_id = cluster_id_from_identity,
	};
	struct trace_ctx trace;

	ret = snat_v4_rev_nat(ctx, &target, &trace, ext_err);
	if (ret != NAT_PUNT_TO_STACK && ret != DROP_NAT_NO_MAPPING) {
		if (IS_ERR(ret))
			return ret;

		/*
		 * RevSNAT succeeded. Identify the remote host using
		 * cluster_id in the rest of the datapath logic.
		 */
		cluster_id = cluster_id_from_identity;
	}

	/* Theoretically, we only need to revalidate data after we
	 * perform revSNAT. However, we observed the mysterious
	 * verifier error in the kernel 4.19 that when we only do
	 * revalidate after the revSNAT, verifier detects an error
	 * for the subsequent read for ip4 pointer. To avoid that,
	 * we always revalidate data here.
	 */
	if (!revalidate_data(ctx, &data, &data_end, &ip4))
		return DROP_INVALID;

	ep = lookup_ip4_endpoint(ip4);
	if (ep) {
		/* We don't support inter-cluster SNAT from host */
		if (ep->flags & ENDPOINT_MASK_HOST_DELIVERY)
			return ipv4_host_delivery(ctx, ip4);

		return ipv4_local_delivery(ctx, ETH_HLEN, src_sec_identity,
					   MARK_MAGIC_IDENTITY, ip4, ep,
					   METRIC_INGRESS, false, true,
					   cluster_id);
	}

	return DROP_UNROUTABLE;
}

__declare_tail(CILIUM_CALL_IPV4_INTER_CLUSTER_REVSNAT)
int tail_handle_inter_cluster_revsnat(struct __ctx_buff *ctx)
{
	int ret;
	__u32 src_sec_identity = ctx_load_and_clear_meta(ctx, CB_SRC_LABEL);
	__s8 ext_err = 0;

	ret = handle_inter_cluster_revsnat(ctx, src_sec_identity, &ext_err);
	if (IS_ERR(ret))
		return send_drop_notify_error_ext(ctx, src_sec_identity, ret, ext_err,
						  METRIC_INGRESS);
	return ret;
}
#endif

static __always_inline int handle_ipv4(struct __ctx_buff *ctx,
				       __u32 *identity,
				       __s8 *ext_err __maybe_unused)
{
	struct remote_endpoint_info *info;
	void *data_end, *data;
	struct iphdr *ip4;
	struct endpoint_info *ep;
	bool decrypted = false;
	bool __maybe_unused is_dsr = false;
	fraginfo_t fraginfo __maybe_unused;
	int ret;

	/* verifier workaround (dereference of modified ctx ptr) */
	if (!revalidate_data_pull(ctx, &data, &data_end, &ip4))
		return DROP_INVALID;

/* If IPv4 fragmentation is disabled
 * AND a IPv4 fragmented packet is received,
 * then drop the packet.
 */
#ifndef ENABLE_IPV4_FRAGMENTS
	fraginfo = ipfrag_encode_ipv4(ip4);
	if (ipfrag_is_fragment(fraginfo))
		return DROP_FRAG_NOSUPPORT;
#endif

#ifdef ENABLE_MULTICAST
	if (IN_MULTICAST(bpf_ntohl(ip4->daddr))) {
		if (mcast_lookup_subscriber_map(&ip4->daddr))
			return tail_call_internal(ctx,
						  CILIUM_CALL_MULTICAST_EP_DELIVERY,
						  ext_err);
	}
#endif /* ENABLE_MULTICAST */

#ifdef ENABLE_NODEPORT
	if (!ctx_skip_nodeport(ctx)) {
		bool punt_to_stack = false;

		ret = nodeport_lb4(ctx, ip4, ETH_HLEN, *identity, &punt_to_stack,
				   ext_err, &is_dsr);
		/* nodeport_lb4() returns with TC_ACT_REDIRECT for
		 * traffic to L7 LB. Policy enforcement needs to take
		 * place after L7 LB has processed the packet, so we
		 * return to stack immediately here with
		 * TC_ACT_REDIRECT.
		 */
		if (ret < 0 || ret == TC_ACT_REDIRECT)
			return ret;
		if (punt_to_stack)
			return ret;
	}
#endif
	if (!revalidate_data(ctx, &data, &data_end, &ip4))
		return DROP_INVALID;

	/* Lookup the source in the ipcache. Before decryption this will be the
	 * outer source IP to get the source node ID. After decryption this
	 * will be the inner source IP to get the source security identity.
	 */
	info = lookup_ip4_remote_endpoint(ip4->saddr, 0);

#ifdef ENABLE_IPSEC
	decrypted = ((ctx->mark & MARK_MAGIC_HOST_MASK) == MARK_MAGIC_DECRYPT);
#endif
	/* If packets are decrypted the key has already been pushed into metadata. */
	if (decrypted) {
		if (info)
			*identity = info->sec_identity;

		cilium_dbg(ctx, info ? DBG_IP_ID_MAP_SUCCEED4 : DBG_IP_ID_MAP_FAILED4,
			   ip4->saddr, *identity);
	} else {
#ifdef ENABLE_VTEP
		{
			struct vtep_key vkey = {};
			struct vtep_value *vtep;

			vkey.vtep_ip = ip4->saddr & VTEP_MASK;
			vtep = map_lookup_elem(&cilium_vtep_map, &vkey);
			if (!vtep)
				goto skip_vtep;
			if (vtep->tunnel_endpoint) {
				if (!identity_is_world_ipv4(*identity))
					return DROP_INVALID_VNI;
			}
		}
skip_vtep:
#endif

#if defined(ENABLE_CLUSTER_AWARE_ADDRESSING) && defined(ENABLE_INTER_CLUSTER_SNAT)
		{
			__u32 cluster_id_from_identity =
				extract_cluster_id_from_identity(*identity);

			/* When we see inter-cluster communication and if
			 * the destination is IPV4_INTER_CLUSTER_SNAT, try
			 * to perform revSNAT. We tailcall from here since
			 * we saw the complexity issue when we added this
			 * logic in-line.
			 */
			if (cluster_id_from_identity != 0 &&
			    cluster_id_from_identity != CLUSTER_ID &&
			    ip4->daddr == IPV4_INTER_CLUSTER_SNAT) {
				ctx_store_meta(ctx, CB_SRC_LABEL, *identity);
				return tail_call_internal(ctx,
							  CILIUM_CALL_IPV4_INTER_CLUSTER_REVSNAT,
							  ext_err);
			}
		}
#endif
		/* See comment at equivalent code in handle_ipv6() */
		if (info && (identity_is_remote_node(*identity) ||
			     (is_dsr && identity_is_world_ipv4(*identity))))
			*identity = info->sec_identity;
	}

#ifdef ENABLE_IPSEC
	if (!decrypted) {
		__u16 node_id;

		/* IPSec is not currently enforce (feature coming soon)
		 * so for now just handle normally
		 */
		if (ip4->protocol != IPPROTO_ESP) {
			update_metrics(ctx_full_len(ctx), METRIC_INGRESS,
				       REASON_PLAINTEXT);
			goto not_esp;
		}

		node_id = lookup_ip4_node_id(ip4->saddr);
		if (!node_id)
			return DROP_NO_NODE_ID;
		set_ipsec_decrypt_mark(ctx, node_id);

		/* To IPSec stack on cilium_vxlan we are going to pass
		 * this up the stack but eth_type_trans has already labeled
		 * this as an OTHERHOST type packet. To avoid being dropped
		 * by IP stack before IPSec can be processed mark as a HOST
		 * packet.
		 */
		ctx_change_type(ctx, PACKET_HOST);

		send_trace_notify(ctx, TRACE_TO_STACK, *identity, UNKNOWN_ID,
				  TRACE_EP_ID_UNKNOWN, ctx->ingress_ifindex,
				  TRACE_REASON_ENCRYPTED, 0, bpf_htons(ETH_P_IP));

		return CTX_ACT_OK;
	}
	ctx->mark = 0;
not_esp:
#endif

#if defined(ENABLE_EGRESS_GATEWAY_COMMON)
	{
		__u32 egress_ifindex = 0;
		__be32 snat_addr, daddr;

		daddr = ip4->daddr;
		if (egress_gw_snat_needed_hook(ip4->saddr, daddr, &snat_addr,
					       &egress_ifindex)) {
			if (snat_addr == EGRESS_GATEWAY_NO_EGRESS_IP)
				return DROP_NO_EGRESS_IP;

			ret = ipv4_l3(ctx, ETH_HLEN, NULL, NULL, ip4);
			if (unlikely(ret != CTX_ACT_OK))
				return ret;

			set_identity_mark(ctx, *identity, MARK_MAGIC_EGW_DONE);

			/* to-netdev@bpf_host handles SNAT, so no need to do it here. */
			return egress_gw_fib_lookup_and_redirect(ctx, snat_addr,
								 daddr, egress_ifindex,
								 ext_err);
		}
	}
#endif /* ENABLE_EGRESS_GATEWAY_COMMON */

#if defined(ENABLE_DSR) && (DSR_ENCAP_MODE == DSR_ENCAP_GENEVE)
	/* Pass incoming packets which will be returned using Geneve DSR
	 * to host-stack for conntrack entry insertion.
	 * Geneve DSR reply packets are processed by the host-stack,
	 * so this logic is needed to prevent the packets from being handled
	 * by netfilter in an unintended way.
	 */
	if (!is_defined(ENABLE_HOST_ROUTING) && is_dsr) {
		ctx_change_type(ctx, PACKET_HOST);
		return CTX_ACT_OK;
	}
#endif

	/* Deliver to local (non-host) endpoint: */
	ep = lookup_ip4_endpoint(ip4);
	if (ep && !(ep->flags & ENDPOINT_MASK_HOST_DELIVERY))
		return ipv4_local_delivery(ctx, ETH_HLEN, *identity, MARK_MAGIC_IDENTITY,
					   ip4, ep, METRIC_INGRESS, false, true, 0);

	ret = overlay_ingress_policy_hook(ctx, ip4, *identity, ext_err);
	if (ret != CTX_ACT_OK)
		return ret;

	/* A packet entering the node from the tunnel and not going to a local
	 * endpoint has to be going to the local host.
	 */

	set_identity_mark(ctx, *identity, MARK_MAGIC_IDENTITY);

	return ipv4_host_delivery(ctx, ip4);
}

__declare_tail(CILIUM_CALL_IPV4_FROM_OVERLAY)
int tail_handle_ipv4(struct __ctx_buff *ctx)
{
	__u32 src_sec_identity = ctx_load_and_clear_meta(ctx, CB_SRC_LABEL);
	__s8 ext_err = 0;
	int ret;

	ret = handle_ipv4(ctx, &src_sec_identity, &ext_err);
	if (IS_ERR(ret))
		return send_drop_notify_error_ext(ctx, src_sec_identity, ret, ext_err,
						  METRIC_INGRESS);
	return ret;
}

#ifdef ENABLE_VTEP
/*
 * ARP responder for ARP requests from VTEP
 * Respond to remote VTEP endpoint with cilium_vxlan MAC
 */
__declare_tail(CILIUM_CALL_ARP)
int tail_handle_arp(struct __ctx_buff *ctx)
{
	struct remote_endpoint_info fake_info = {0};
	union macaddr mac = THIS_INTERFACE_MAC;
	union macaddr smac;
	struct trace_ctx trace = {
		.reason = TRACE_REASON_CT_REPLY,
		.monitor = TRACE_PAYLOAD_LEN,
	};
	__be32 sip;
	__be32 tip;
	int ret;
	struct bpf_tunnel_key key = {};
	struct vtep_key vkey = {};
	struct vtep_value *info;
	__u32 key_size;

	key_size = TUNNEL_KEY_WITHOUT_SRC_IP;
	if (unlikely(ctx_get_tunnel_key(ctx, &key, key_size, 0) < 0))
		return send_drop_notify_error(ctx, UNKNOWN_ID, DROP_NO_TUNNEL_KEY, METRIC_INGRESS);

	if (!arp_validate(ctx, &mac, &smac, &sip, &tip) || !__lookup_ip4_endpoint(tip))
		goto pass_to_stack;
	vkey.vtep_ip = sip & VTEP_MASK;
	info = map_lookup_elem(&cilium_vtep_map, &vkey);
	if (!info)
		goto pass_to_stack;

	ret = arp_prepare_response(ctx, &mac, tip, &smac, sip);
	if (unlikely(ret != 0))
		return send_drop_notify_error(ctx, UNKNOWN_ID, ret, METRIC_EGRESS);
	if (info->tunnel_endpoint) {
		fake_info.tunnel_endpoint.ip4 = info->tunnel_endpoint;
		fake_info.flag_has_tunnel_ep = true;
		ret = __encap_and_redirect_with_nodeid(ctx, &fake_info,
						       LOCAL_NODE_ID, WORLD_IPV4_ID,
						       WORLD_IPV4_ID, &trace,
						       bpf_htons(ETH_P_ARP));
		if (IS_ERR(ret))
			goto drop_err;

		return ret;
	}

	ret = DROP_UNKNOWN_L3;
drop_err:
	return send_drop_notify_error(ctx, UNKNOWN_ID, ret, METRIC_EGRESS);

pass_to_stack:
	send_trace_notify(ctx, TRACE_TO_STACK, UNKNOWN_ID, UNKNOWN_ID,
			  TRACE_EP_ID_UNKNOWN, ctx->ingress_ifindex,
			  trace.reason, trace.monitor, bpf_htons(ETH_P_ARP));
	return CTX_ACT_OK;
}
#endif /* ENABLE_VTEP */

#endif /* ENABLE_IPV4 */

#ifdef ENABLE_IPSEC
static __always_inline bool is_esp(struct __ctx_buff *ctx, __u16 proto)
{
	void *data, *data_end;
	__u8 protocol = 0;
	struct ipv6hdr *ip6 __maybe_unused;
	struct iphdr *ip4 __maybe_unused;

	switch (proto) {
#ifdef ENABLE_IPV6
	case bpf_htons(ETH_P_IPV6):
		if (!revalidate_data_pull(ctx, &data, &data_end, &ip6))
			return false;
		protocol = ip6->nexthdr;
		break;
#endif
#ifdef ENABLE_IPV4
	case bpf_htons(ETH_P_IP):
		if (!revalidate_data_pull(ctx, &data, &data_end, &ip4))
			return false;
		protocol = ip4->protocol;
		break;
#endif
	default:
		return false;
	}

	return protocol == IPPROTO_ESP;
}
#endif /* ENABLE_IPSEC */

/* Attached to the ingress of cilium_vxlan/cilium_geneve to execute on packets
 * entering the node via the tunnel.
 */
__section_entry
int cil_from_overlay(struct __ctx_buff *ctx)
{
	__u32 src_sec_identity = 0;
	__s8 ext_err = 0;
	bool decrypted = false;
	__u16 proto;
	int ret;

	bpf_clear_meta(ctx);
	ctx_skip_nodeport_clear(ctx);

	if (!validate_ethertype(ctx, &proto)) {
		/* Pass unknown traffic to the stack */
		ret = CTX_ACT_OK;
		goto out;
	}

/* We need to handle following possible packets come to this program
 *
 * 1. ESP packets coming from overlay (encrypted and not marked)
 * 2. Non-ESP packets coming from overlay (plain and not marked)
 * 3. Non-ESP packets coming from stack re-inserted by xfrm (plain
 *    and marked with MARK_MAGIC_DECRYPT. Only in IPSec mode.)
 *
 * 1. will be traced with TRACE_REASON_ENCRYPTED
 * 2. will be traced without TRACE_REASON_ENCRYPTED
 * 3. will be traced without TRACE_REASON_ENCRYPTED
 *
 * Note that 1. contains the ESP packets someone else generated.
 * In that case, we trace it as "encrypted", but it doesn't mean
 * "encrypted by Cilium".
 *
 * When IPSec is disabled, we won't use TRACE_REASON_ENCRYPTED even
 * if the packets are ESP, because it doesn't matter for the
 * non-IPSec mode.
 */
#ifdef ENABLE_IPSEC
	decrypted = ((ctx->mark & MARK_MAGIC_HOST_MASK) == MARK_MAGIC_DECRYPT);
#endif

	switch (proto) {
#if defined(ENABLE_IPV4) || defined(ENABLE_IPV6)
 #ifdef ENABLE_IPV6
	case bpf_htons(ETH_P_IPV6):
 #endif
 #ifdef ENABLE_IPV4
	case bpf_htons(ETH_P_IP):
 #endif
		/* If packets are decrypted the key has already been pushed into metadata. */
		if (!decrypted) {
			struct bpf_tunnel_key key = {};

			ret = get_tunnel_key(ctx, &key);
			if (unlikely(ret < 0))
				goto out;
			cilium_dbg(ctx, DBG_DECAP, key.tunnel_id, key.tunnel_label);

			src_sec_identity = get_id_from_tunnel_id(key.tunnel_id, proto);

			/* Any node encapsulating will map any HOST_ID source to be
			 * presented as REMOTE_NODE_ID, therefore any attempt to signal
			 * HOST_ID as source from a remote node can be dropped.
			 */
			if (src_sec_identity == HOST_ID) {
				ret = DROP_INVALID_IDENTITY;
				goto out;
			}

			ctx_store_meta(ctx, CB_SRC_LABEL, src_sec_identity);
		}
		break;
#endif /* ENABLE_IPV4 || ENABLE_IPV6 */
	default:
		break;
	}

#ifdef ENABLE_IPSEC
	if (is_esp(ctx, proto))
		send_trace_notify(ctx, TRACE_FROM_OVERLAY, src_sec_identity, UNKNOWN_ID,
				  TRACE_EP_ID_UNKNOWN, ctx->ingress_ifindex,
				  TRACE_REASON_ENCRYPTED, 0, proto);
	else
#endif
	{
		enum trace_point obs_point = TRACE_FROM_OVERLAY;

		/* Non-ESP packet marked with MARK_MAGIC_DECRYPT is a packet
		 * re-inserted from the stack.
		 */
		if (decrypted)
			obs_point = TRACE_FROM_STACK;

		send_trace_notify(ctx, obs_point, src_sec_identity, UNKNOWN_ID,
				  TRACE_EP_ID_UNKNOWN, ctx->ingress_ifindex,
				  TRACE_REASON_UNKNOWN, TRACE_PAYLOAD_LEN, proto);
	}

	switch (proto) {
	case bpf_htons(ETH_P_IPV6):
#ifdef ENABLE_IPV6
		ret = tail_call_internal(ctx, CILIUM_CALL_IPV6_FROM_OVERLAY, &ext_err);
#else
		ret = DROP_UNKNOWN_L3;
#endif
		break;

	case bpf_htons(ETH_P_IP):
#ifdef ENABLE_IPV4
		ret = tail_call_internal(ctx, CILIUM_CALL_IPV4_FROM_OVERLAY, &ext_err);
#else
		ret = DROP_UNKNOWN_L3;
#endif
		break;

#ifdef ENABLE_VTEP
	case bpf_htons(ETH_P_ARP):
		ret = tail_call_internal(ctx, CILIUM_CALL_ARP, &ext_err);
		break;
#endif

	default:
		/* Pass unknown traffic to the stack */
		ret = CTX_ACT_OK;
	}
out:
	if (IS_ERR(ret))
		return send_drop_notify_error_ext(ctx, src_sec_identity, ret,
						  ext_err, METRIC_INGRESS);
	return ret;
}

/* Attached to the egress of cilium_vxlan/cilium_geneve to execute on packets
 * leaving the node via the tunnel.
 */
__section_entry
int cil_to_overlay(struct __ctx_buff *ctx)
{
	bool snat_done __maybe_unused = ctx_snat_done(ctx);
	struct trace_ctx __maybe_unused trace;
	struct bpf_tunnel_key tunnel_key = {};
	__u32 src_sec_identity = UNKNOWN_ID;
	int ret = TC_ACT_OK;
	__u32 cluster_id __maybe_unused = 0;
	__be16 __maybe_unused proto = 0;
	__s8 ext_err = 0;

	bpf_clear_meta(ctx);

	/* Load the ethertype just once: */
	validate_ethertype(ctx, &proto);

#ifdef ENABLE_BANDWIDTH_MANAGER
	/* In tunneling mode, we should do this as close as possible to the
	 * phys dev where FQ runs, but the issue is that the aggregate state
	 * (in queue_mapping) is overridden on tunnel xmit. Hence set the
	 * timestamp already here. The tunnel dev has noqueue qdisc, so as
	 * tradeoff it's close enough.
	 */
	ret = edt_sched_departure(ctx, proto);
	/* No send_drop_notify_error() here given we're rate-limiting. */
	if (ret < 0) {
		update_metrics(ctx_full_len(ctx), METRIC_EGRESS, (__u8)-ret);
		return CTX_ACT_DROP;
	}
#endif

	/* This must be after above ctx_snat_done, since the MARK_MAGIC_CLUSTER_ID
	 * is a super set of the MARK_MAGIC_SNAT_DONE. They will never be used together,
	 * but SNAT check should always take presedence.
	 */
#ifdef ENABLE_CLUSTER_AWARE_ADDRESSING
	cluster_id = ctx_get_cluster_id_mark(ctx);
#endif

	/* We might see some unexpected packets without tunnel_key (eg. IPv6 ND).
	 * No need to worry, the geneve/vxlan kernel drivers will drop them.
	 */
	if (!ctx_get_tunnel_key(ctx, &tunnel_key, TUNNEL_KEY_WITHOUT_SRC_IP, 0))
		src_sec_identity = get_id_from_tunnel_id(tunnel_key.tunnel_id,
							 ctx_get_protocol(ctx));
#ifdef ENABLE_IPSEC
	if (is_esp(ctx, proto))
		set_identity_mark(ctx, src_sec_identity, MARK_MAGIC_OVERLAY_ENCRYPTED);
	else
#endif
		set_identity_mark(ctx, src_sec_identity, MARK_MAGIC_OVERLAY);

#ifdef ENABLE_NODEPORT
	if (snat_done) {
		ret = CTX_ACT_OK;
		goto out;
	}

	ret = handle_nat_fwd(ctx, cluster_id, src_sec_identity, proto, false, &trace, &ext_err);
out:
#endif
	if (IS_ERR(ret))
		return send_drop_notify_error_ext(ctx, src_sec_identity, ret, ext_err,
						  METRIC_EGRESS);
	return ret;
}

BPF_LICENSE("Dual BSD/GPL");
