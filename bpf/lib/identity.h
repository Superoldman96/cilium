/* SPDX-License-Identifier: (GPL-2.0-only OR BSD-2-Clause) */
/* Copyright Authors of Cilium */

#pragma once

#include "dbg.h"

static __always_inline bool identity_in_range(__u32 identity, __u32 range_start, __u32 range_end)
{
	return range_start <= identity && identity <= range_end;
}

#define IDENTITY_LOCAL_SCOPE_MASK 0xFF000000
#define IDENTITY_LOCAL_SCOPE_REMOTE_NODE 0x02000000

static __always_inline bool identity_is_host(__u32 identity)
{
	return identity == HOST_ID;
}

static __always_inline bool identity_is_remote_node(__u32 identity)
{
	/* KUBE_APISERVER_NODE_ID is the reserved identity that corresponds to
	 * the labels 'reserved:remote-node' and 'reserved:kube-apiserver'. As
	 * such, if it is ever used for determining the identity of a node in
	 * the cluster, then routing decisions and so on should be made the
	 * same way as for REMOTE_NODE_ID. If we ever assign unique identities
	 * to each node in the cluster, then we'll probably need to convert
	 * the implementation here into a map to select any of the possible
	 * identities. But for now, this is good enough to capture the notion
	 * of 'remote nodes in the cluster' for routing decisions.
	 *
	 * Remote nodes may also have, instead, an identity allocated from the
	 * remote node identity scope, which is identified by the top 8 bits
	 * being 0x02.
	 *
	 * Note that kube-apiserver policy is handled entirely separately by
	 * the standard policymap enforcement logic and has no relationship to
	 * the identity as used here. If the apiserver is outside the cluster,
	 * then the KUBE_APISERVER_NODE_ID case should not ever be hit.
	 */
	return identity == REMOTE_NODE_ID ||
		identity == KUBE_APISERVER_NODE_ID ||
		(identity & IDENTITY_LOCAL_SCOPE_MASK) == IDENTITY_LOCAL_SCOPE_REMOTE_NODE;
}

static __always_inline bool identity_is_node(__u32 identity)
{
	return identity_is_host(identity) || identity_is_remote_node(identity);
}

/**
 * identity_is_reserved is used to determine whether an identity is one of the
 * reserved identities that are not handed out to endpoints.
 *
 * Specifically, it should return true if the identity is one of these:
 * - IdentityUnknown
 * - ReservedIdentityHost
 * - ReservedIdentityWorld
 * - ReservedIdentityWorldIPv4
 * - ReservedIdentityWorldIPv6
 * - ReservedIdentityRemoteNode
 * - ReservedIdentityKubeAPIServer
 *
 * The following identities are given to endpoints so return false for these:
 * - ReservedIdentityUnmanaged
 * - ReservedIdentityHealth
 * - ReservedIdentityInit
 *
 * Identities 128 and higher are guaranteed to be generated based on user input.
 */
static __always_inline bool identity_is_reserved(__u32 identity)
{
#if defined ENABLE_IPV4 && defined ENABLE_IPV6
		return identity < UNMANAGED_ID || identity_is_remote_node(identity) ||
			identity == WORLD_IPV4_ID || identity == WORLD_IPV6_ID;
#else
		return identity < UNMANAGED_ID || identity_is_remote_node(identity);
#endif
}

/**
 * identity_is_world_ipv4 is used to determine whether an identity is the world-ipv4
 * reserved identity.
 *
 * Specifically, it should return true if the identity is one of these:
 * - ReservedIdentityWorld
 * - ReservedIdentityWorldIPv4
 */
static __always_inline bool identity_is_world_ipv4(__u32 identity)
{
#if defined ENABLE_IPV4 && defined ENABLE_IPV6
		return identity == WORLD_ID || identity == WORLD_IPV4_ID;
#else
		return identity == WORLD_ID;
#endif
}

/**
 * identity_is_world_ipv6 is used to determine whether an identity is the world-ipv6
 * reserved identity.
 *
 * Specifically, it should return true if the identity is one of these:
 * - ReservedIdentityWorld
 * - ReservedIdentityWorldIPv6
 */
static __always_inline bool identity_is_world_ipv6(__u32 identity)
{
#if defined ENABLE_IPV4 && defined ENABLE_IPV6
		return identity == WORLD_ID || identity == WORLD_IPV6_ID;
#else
		return identity == WORLD_ID;
#endif
}

/**
 * identity_is_cidr_range is used to determine whether an identity is assigned
 * to a CIDR range.
 */
static __always_inline bool identity_is_cidr_range(__u32 identity)
{
	return identity_in_range(identity, CIDR_IDENTITY_RANGE_START, CIDR_IDENTITY_RANGE_END);
}

/**
 * identity_is_cluster is used to determine whether an identity is assigned to
 * an entity inside the cluster.
 *
 * This function will return false for:
 * - ReservedIdentityWorld
 * - ReservedIdentityWorldIPv4
 * - ReservedIdentityWorldIPv6
 * - an identity in the CIDR range
 *
 * This function will return true for:
 * - ReservedIdentityHost
 * - ReservedIdentityUnmanaged
 * - ReservedIdentityHealth
 * - ReservedIdentityInit
 * - ReservedIdentityRemoteNode
 * - ReservedIdentityKubeAPIServer
 * - ReservedIdentityIngress
 * - all other identifies
 */
static __always_inline bool identity_is_cluster(__u32 identity)
{
#if defined ENABLE_IPV4 && defined ENABLE_IPV6
	if (identity == WORLD_ID || identity == WORLD_IPV4_ID || identity == WORLD_IPV6_ID)
		return false;
#else
	if (identity == WORLD_ID)
		return false;
#endif

	if (identity_is_cidr_range(identity))
		return false;

	return true;
}

#if __ctx_is == __ctx_skb
static __always_inline __u32 inherit_identity_from_host(struct __ctx_buff *ctx, __u32 *identity)
{
	__u32 magic = ctx->mark & MARK_MAGIC_HOST_MASK;

	/* Packets from the ingress proxy must skip the proxy when the
	 * destination endpoint evaluates the policy. As the packet would loop
	 * and/or the connection be reset otherwise.
	 */
	if (magic == MARK_MAGIC_PROXY_INGRESS) {
		*identity = get_identity(ctx);
		ctx->tc_index |= TC_INDEX_F_FROM_INGRESS_PROXY;
	/* (Return) packets from the egress proxy must skip the redirection to
	 * the proxy, as the packet would loop and/or the connection be reset
	 * otherwise.
	 */
	} else if (magic == MARK_MAGIC_PROXY_EGRESS) {
		*identity = get_identity(ctx);
		ctx->tc_index |= TC_INDEX_F_FROM_EGRESS_PROXY;
	} else if (magic == MARK_MAGIC_IDENTITY) {
		*identity = get_identity(ctx);
	} else if (magic == MARK_MAGIC_HOST) {
		*identity = HOST_ID;
#ifdef ENABLE_IPSEC
	} else if (magic == MARK_MAGIC_ENCRYPT) {
		*identity = ctx_load_meta(ctx, CB_ENCRYPT_IDENTITY);
#endif
	} else {
#if defined ENABLE_IPV4 && defined ENABLE_IPV6
		__u16 proto = ctx_get_protocol(ctx);

		if (proto == bpf_htons(ETH_P_IP))
			*identity = WORLD_IPV4_ID;
		else if (proto == bpf_htons(ETH_P_IPV6))
			*identity = WORLD_IPV6_ID;
		else
			*identity = WORLD_ID;
#else
		*identity = WORLD_ID;
#endif
	}

	/* Reset packet mark to avoid hitting routing rules again */
	ctx->mark = 0;

	cilium_dbg(ctx, DBG_INHERIT_IDENTITY, *identity, 0);

	return magic;
}
#endif /* __ctx_is == __ctx_skb */

/**
 * identity_is_local is used to determine whether an identity is locally
 * allocated.
 */
static __always_inline bool identity_is_local(__u32 identity)
{
	return (identity & IDENTITY_LOCAL_SCOPE_MASK) != 0;
}
