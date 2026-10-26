//go:build linux || darwin

package flowbroker

import (
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// flowMarker is the one data byte that carries each passed flow descriptor;
// keepaliveMarker carries none and only proves the broker is responsive.
const (
	flowMarker      = 'F'
	keepaliveMarker = 'K'
)

// fileConn is a connection whose descriptor can be duplicated: Unix sockets
// from tests and the loopback TCP connections the extension opens.
type fileConn interface {
	net.Conn
	File() (*os.File, error)
}

// passConn sends a duplicate of conn's descriptor over channel. The caller
// still owns conn and closes it afterwards; the receiver owns the duplicate.
func passConn(channel *net.UnixConn, conn fileConn) error {
	file, err := conn.File()
	if err != nil {
		return err
	}
	defer file.Close()
	n, oobn, err := channel.WriteMsgUnix([]byte{flowMarker}, unix.UnixRights(int(file.Fd())), nil)
	if err != nil {
		return err
	}
	if n != 1 || oobn == 0 {
		return errors.New("flow descriptor was not sent")
	}
	return nil
}

// receiveConn reads one message from channel: a passed flow descriptor, or a
// keepalive (nil connection, nil error).
func receiveConn(channel *net.UnixConn) (net.Conn, error) {
	data := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	n, oobn, _, _, err := channel.ReadMsgUnix(data, oob)
	if err != nil {
		return nil, err
	}
	if n == 1 && data[0] == keepaliveMarker && oobn == 0 {
		return nil, nil
	}
	if n != 1 || data[0] != flowMarker {
		return nil, errors.New("flow channel message is invalid")
	}
	messages, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(messages) != 1 {
		return nil, errors.New("flow channel carried no descriptor")
	}
	fds, err := unix.ParseUnixRights(&messages[0])
	if err != nil || len(fds) != 1 {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
		return nil, errors.New("flow channel carried an unexpected descriptor count")
	}
	file := os.NewFile(uintptr(fds[0]), "flow")
	defer file.Close()
	return net.FileConn(file)
}
