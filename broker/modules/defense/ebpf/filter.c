//go:build ignore

#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/ip.h>

#define bpf_htons(x) ((__builtin_constant_p(x) ? \
                      ((((x) & 0xff) << 8) | (((x) & 0xff00) >> 8)) : \
                      __builtin_bswap16(x)))

#define __uint(name, val) int (*name)[val]
#define __type(name, val) typeof(val) *name
#define SEC(name) __attribute__((section(name), used))

// Manually define the eBPF helper we need so we don't require libbpf-dev
static void *(*bpf_map_lookup_elem)(void *map, const void *key) = (void *) 1;

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 10240);
    __type(key, __u32);   // IPv4 address
    __type(value, __u8);  // Dummy value
} ip_blocklist SEC(".maps");

SEC("xdp")
int xdp_filter(struct xdp_md *ctx) {
    void *data_end = (void *)(long)ctx->data_end;
    void *data = (void *)(long)ctx->data;

    struct ethhdr *eth = data;
    if ((void *)(eth + 1) > data_end)
        return XDP_PASS;

    if (eth->h_proto != bpf_htons(ETH_P_IP))
        return XDP_PASS;

    struct iphdr *ip = (void *)(eth + 1);
    if ((void *)(ip + 1) > data_end)
        return XDP_PASS;

    __u32 src_ip = ip->saddr;

    __u8 *banned = bpf_map_lookup_elem(&ip_blocklist, &src_ip);
    if (banned) {
        return XDP_DROP;
    }

    return XDP_PASS;
}

char __license[] SEC("license") = "Dual MIT/GPL";
