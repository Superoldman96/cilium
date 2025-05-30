/* SPDX-License-Identifier: (GPL-2.0-only OR BSD-2-Clause) */
/* Copyright Authors of Cilium */

#pragma once

#include <bpf/api.h>

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(__u32));
	__uint(value_size, sizeof(__u32));
	__uint(pinning, LIBBPF_PIN_BY_NAME);
} cilium_events __section_maps_btf;

#ifdef EVENTS_MAP_RATE_LIMIT
#ifndef EVENTS_MAP_BURST_LIMIT
#define EVENTS_MAP_BURST_LIMIT EVENTS_MAP_RATE_LIMIT
#endif
#endif

