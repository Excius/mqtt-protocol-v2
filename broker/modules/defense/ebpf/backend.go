package ebpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go filter filter.c -- -I/usr/include/x86_64-linux-gnu -I.

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"github.com/cilium/ebpf/link"
)

var (
	// ErrNotEnabled is returned if eBPF is not successfully loaded.
	ErrNotEnabled = errors.New("eBPF filter is not enabled")
)

// Backend defines the interface for blocking IPs at the kernel level.
type Backend interface {
	Init(log *slog.Logger, ifaceName string) error
	BlockIP(ip string) error
	Close() error
}

type ebpfBackend struct {
	objs  filterObjects
	xlink link.Link
	log   *slog.Logger
	mu    sync.Mutex
}

// NewBackend returns a new eBPF backend.
func NewBackend() Backend {
	return &ebpfBackend{}
}

func (b *ebpfBackend) Init(log *slog.Logger, ifaceName string) error {
	b.log = log

	if err := loadFilterObjects(&b.objs, nil); err != nil {
		return fmt.Errorf("loading objects: %w", err)
	}

	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		b.log.Warn("failed to find network interface for XDP, falling back to application-layer only", "interface", ifaceName, "error", err)
	} else {
		xl, err := link.AttachXDP(link.XDPOptions{
			Program:   b.objs.XdpFilter,
			Interface: iface.Index,
		})
		if err != nil {
			b.log.Warn("failed to attach XDP program (requires root), falling back to application-layer only", "error", err)
		} else {
			b.xlink = xl
			b.log.Info("eBPF XDP filter attached successfully to interface", "interface", ifaceName)
		}
	}

	b.log.Info("eBPF map initialized", "entries", 10240)
	return nil
}

func (b *ebpfBackend) BlockIP(ipStr string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return fmt.Errorf("invalid IP: %s", ipStr)
	}

	// We only support IPv4 in this simple XDP program.
	ipv4 := ip.To4()
	if ipv4 == nil {
		return fmt.Errorf("only IPv4 is supported for eBPF blocking")
	}

	var key uint32
	key = uint32(ipv4[0]) | uint32(ipv4[1])<<8 | uint32(ipv4[2])<<16 | uint32(ipv4[3])<<24

	var value uint8 = 1

	if b.objs.IpBlocklist == nil {
		return ErrNotEnabled
	}

	if err := b.objs.IpBlocklist.Put(key, value); err != nil {
		return fmt.Errorf("failed to update bpf map: %w", err)
	}

	b.log.Warn("IP blocked in kernel via eBPF", "ip", ipStr)
	return nil
}

func (b *ebpfBackend) Close() error {
	if b.xlink != nil {
		b.xlink.Close()
	}
	return b.objs.Close()
}
