package serverhost

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
)

type runtimeInterfaceAddress struct {
	name    string
	address netip.Addr
}

func discoverRuntimeConnectTargets(listenAddress string) ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	addresses := make([]runtimeInterfaceAddress, 0)
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 ||
			networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		assigned, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, value := range assigned {
			prefix, err := netip.ParsePrefix(value.String())
			if err != nil {
				continue
			}
			addresses = append(addresses, runtimeInterfaceAddress{
				name: networkInterface.Name, address: prefix.Addr().Unmap(),
			})
		}
	}
	return runtimeConnectTargets(listenAddress, addresses)
}

func runtimeConnectTargets(
	listenAddress string,
	interfaces []runtimeInterfaceAddress,
) ([]string, error) {
	listen, err := netip.ParseAddrPort(listenAddress)
	if err != nil || listen.Port() == 0 {
		return nil, errors.New("Runtime Server listen address is invalid")
	}
	address := listen.Addr().Unmap()
	if !address.IsUnspecified() {
		return []string{netip.AddrPortFrom(address, listen.Port()).String()}, nil
	}
	candidates := slices.Clone(interfaces)
	slices.SortStableFunc(candidates, func(left, right runtimeInterfaceAddress) int {
		if rank := runtimeInterfaceRank(left.name) - runtimeInterfaceRank(right.name); rank != 0 {
			return rank
		}
		if rank := runtimeAddressRank(left.address) - runtimeAddressRank(right.address); rank != 0 {
			return rank
		}
		return left.address.Compare(right.address)
	})
	targets := make([]string, 0, len(candidates))
	seen := make(map[netip.Addr]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate.address = candidate.address.Unmap()
		if !usableRuntimeAddress(candidate.address) {
			continue
		}
		if _, duplicate := seen[candidate.address]; duplicate {
			continue
		}
		seen[candidate.address] = struct{}{}
		targets = append(targets, netip.AddrPortFrom(candidate.address, listen.Port()).String())
	}
	if len(targets) == 0 {
		targets = append(targets, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), listen.Port()).String())
	}
	return targets, nil
}

func usableRuntimeAddress(address netip.Addr) bool {
	return address.IsValid() && !address.IsUnspecified() && !address.IsLoopback() &&
		!address.IsLinkLocalUnicast() && !address.IsMulticast()
}

func runtimeAddressRank(address netip.Addr) int {
	address = address.Unmap()
	if address.Is4() && address.IsPrivate() {
		return 0
	}
	if address.Is4() {
		return 1
	}
	if address.IsPrivate() {
		return 2
	}
	return 3
}

func runtimeInterfaceRank(name string) int {
	lower := strings.ToLower(name)
	for _, prefix := range []string{"utun", "tun", "tap", "docker", "bridge", "br-", "veth", "vmnet"} {
		if strings.HasPrefix(lower, prefix) {
			return 1
		}
	}
	return 0
}
