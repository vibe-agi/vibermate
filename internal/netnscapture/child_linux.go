//go:build linux

package netnscapture

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	tunName = "vmtun0"
	tunMTU  = 1500
	// controlFD is the socketpair end the launcher passes as ExtraFiles[0].
	controlFD = 3
)

// RunChild is the helper that vibermate re-executes inside a fresh user,
// network and mount namespace (ChildCommand). It brings up loopback and a TUN
// device routing all IPv4, points DNS at the run's resolver, hands the TUN
// device to the launcher, waits for its go-ahead, drops its namespace
// capabilities and executes the Agent. arguments are the Agent's path
// followed by its argv.
func RunChild(arguments []string) error {
	// Capabilities are per thread: clearing the ambient set and executing the
	// Agent must happen on the same OS thread, or the Agent could inherit
	// another thread's namespace capabilities.
	runtime.LockOSThread()
	if len(arguments) < 2 {
		return errors.New("namespace helper needs the Agent path and argv")
	}
	control := os.NewFile(controlFD, "vibermate-namespace-control")
	if control == nil {
		return errors.New("namespace helper has no control socket")
	}
	defer control.Close()
	tun, err := configureNetwork()
	if err != nil {
		reportRestricted(control, err)
		return fmt.Errorf("configure the capture network: %w", err)
	}
	if err := redirectResolver(); err != nil {
		unix.Close(tun)
		reportRestricted(control, err)
		return fmt.Errorf("point DNS at the capture resolver: %w", err)
	}
	if err := unix.Sendmsg(int(control.Fd()), []byte{'T'}, unix.UnixRights(tun), nil, 0); err != nil {
		return fmt.Errorf("hand the TUN device to the launcher: %w", err)
	}
	_ = unix.Close(tun)
	ready := make([]byte, 1)
	if n, err := control.Read(ready); err != nil || n != 1 || ready[0] != 'G' {
		return errors.New("the launcher did not start capture")
	}
	_ = control.Close()
	// Capabilities exist only inside these namespaces; the Agent gets none.
	// With the ambient set empty, executing as the (non-root) mapped user
	// leaves the Agent no permitted or effective capabilities.
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("drop namespace capabilities: %w", err)
	}
	return syscall.Exec(arguments[0], arguments[1:], os.Environ())
}

// reportRestricted tells the launcher, before the helper exits, that the
// namespace was created without usable capabilities.
func reportRestricted(control *os.File, err error) {
	if errors.Is(err, unix.EPERM) {
		_, _ = control.Write([]byte{'R'})
	}
}

type ifreqFlags struct {
	name  [unix.IFNAMSIZ]byte
	flags uint16
	_     [22]byte
}

type ifreqAddress struct {
	name    [unix.IFNAMSIZ]byte
	address unix.RawSockaddrInet4
	_       [8]byte
}

type ifreqMTU struct {
	name [unix.IFNAMSIZ]byte
	mtu  int32
	_    [20]byte
}

func ioctl(fd int, request uintptr, argument unsafe.Pointer) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(argument)); errno != 0 {
		return errno
	}
	return nil
}

func interfaceName(name string) [unix.IFNAMSIZ]byte {
	var buffer [unix.IFNAMSIZ]byte
	copy(buffer[:], name)
	return buffer
}

func setUp(socket int, name string) error {
	request := ifreqFlags{name: interfaceName(name)}
	if err := ioctl(socket, unix.SIOCGIFFLAGS, unsafe.Pointer(&request)); err != nil {
		return err
	}
	request.flags |= unix.IFF_UP
	return ioctl(socket, unix.SIOCSIFFLAGS, unsafe.Pointer(&request))
}

func setIPv4(socket int, name string, request uintptr, address [4]byte) error {
	value := ifreqAddress{name: interfaceName(name)}
	value.address.Family = unix.AF_INET
	value.address.Addr = address
	return ioctl(socket, request, unsafe.Pointer(&value))
}

// configureNetwork runs in the new network namespace with CAP_NET_ADMIN.
func configureNetwork() (int, error) {
	socket, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(socket)
	if err := setUp(socket, "lo"); err != nil {
		return -1, fmt.Errorf("bring up loopback: %w", err)
	}
	tun, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	request := ifreqFlags{name: interfaceName(tunName), flags: unix.IFF_TUN | unix.IFF_NO_PI}
	if err := ioctl(tun, unix.TUNSETIFF, unsafe.Pointer(&request)); err != nil {
		unix.Close(tun)
		return -1, fmt.Errorf("create %s: %w", tunName, err)
	}
	mtu := ifreqMTU{name: interfaceName(tunName), mtu: tunMTU}
	steps := []struct {
		what string
		err  func() error
	}{
		{"set MTU", func() error { return ioctl(socket, unix.SIOCSIFMTU, unsafe.Pointer(&mtu)) }},
		{"set address", func() error { return setIPv4(socket, tunName, unix.SIOCSIFADDR, tunAddress.As4()) }},
		{"set netmask", func() error {
			return setIPv4(socket, tunName, unix.SIOCSIFNETMASK, [4]byte{255, 254, 0, 0})
		}},
		{"bring up", func() error { return setUp(socket, tunName) }},
		{"add default route", func() error { return addDefaultRoute(socket) }},
	}
	for _, step := range steps {
		if err := step.err(); err != nil {
			unix.Close(tun)
			return -1, fmt.Errorf("%s %s: %w", step.what, tunName, err)
		}
	}
	return tun, nil
}

// rtentry mirrors struct rtentry from <net/route.h> on 64-bit Linux.
type rtentry struct {
	pad1    uint64
	dst     unix.RawSockaddrInet4
	gateway unix.RawSockaddrInet4
	genmask unix.RawSockaddrInet4
	flags   uint16
	pad2    int16
	pad3    uint64
	pad4    uintptr
	metric  int16
	dev     *byte
	mtu     uint64
	window  uint64
	irtt    uint16
}

func addDefaultRoute(socket int) error {
	device := append([]byte(tunName), 0)
	route := rtentry{flags: unix.RTF_UP, dev: &device[0]}
	route.dst.Family = unix.AF_INET
	route.gateway.Family = unix.AF_INET
	route.genmask.Family = unix.AF_INET
	return ioctl(socket, unix.SIOCADDRT, unsafe.Pointer(&route))
}

// redirectResolver bind-mounts a resolv.conf naming the capture resolver
// over the system one, in this mount namespace only.
func redirectResolver() error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	target, err := filepath.EvalSymlinks("/etc/resolv.conf")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "vibermate-resolv-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := fmt.Fprintf(file, "# Written by vibermate run --transparent\nnameserver %s\noptions edns0\n", resolverAddress); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(0o644); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return unix.Mount(file.Name(), target, "", unix.MS_BIND, "")
}
