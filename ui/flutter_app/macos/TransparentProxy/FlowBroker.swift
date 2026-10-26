import Darwin
import Foundation

enum FlowBrokerError: Error {
  case invalidConfiguration, invalidFrame, invalidReply, timedOut, endOfStream
  case socket(Int32)
}

struct FlowBrokerConfiguration {
  let port: UInt16
  let secret: String

  init(options: [String: AnyObject]?) throws {
    guard let number = options?["flowPort"] as? NSNumber,
          CFGetTypeID(number) != CFBooleanGetTypeID(),
          number.doubleValue.isFinite,
          number.doubleValue >= 1, number.doubleValue <= 65535,
          number.doubleValue.rounded(.towardZero) == number.doubleValue,
          let secret = options?["flowSecret"] as? String,
          secret.utf8.count >= 32, secret.utf8.count <= 512 else {
      throw FlowBrokerError.invalidConfiguration
    }
    // Check the encoded size too: JSON escaping can expand a short string.
    guard (try? brokerFrame(["v": 1, "pid": Int32.max, "secret": secret], limit: 512)) != nil else {
      throw FlowBrokerError.invalidConfiguration
    }
    self.port = UInt16(number.intValue)
    self.secret = secret
  }
}

struct FlowDestination {
  let host: String
  let port: UInt16
  let remoteHostname: String?

  var isLoopback: Bool {
    if host.lowercased() == "localhost" { return true }
    var ipv4 = in_addr()
    if inet_pton(AF_INET, host, &ipv4) == 1 {
      return UInt32(bigEndian: ipv4.s_addr) >> 24 == 127
    }
    var ipv6 = in6_addr()
    guard let first = host.split(separator: "%", maxSplits: 1).first else { return false }
    let address = String(first)
    guard inet_pton(AF_INET6, address, &ipv6) == 1 else { return false }
    return withUnsafeBytes(of: ipv6) { bytes in
      let loopback = bytes.prefix(15).allSatisfy { $0 == 0 } && bytes[15] == 1
      let mapped = bytes.prefix(10).allSatisfy { $0 == 0 }
        && bytes[10] == 255 && bytes[11] == 255 && bytes[12] == 127
      return loopback || mapped
    }
  }

  func header() throws -> Data {
    guard !host.isEmpty, port > 0 else { throw FlowBrokerError.invalidFrame }
    let destination = host.contains(":") ? "[\(host)]:\(port)" : "\(host):\(port)"
    var fields: [String: Any] = ["v": 1, "proto": "tcp", "dst": destination]
    if let remoteHostname = remoteHostname, !remoteHostname.isEmpty {
      fields["host"] = remoteHostname
    }
    return try brokerFrame(fields, limit: 1024)
  }
}

struct FlowBroker {
  let configuration: FlowBrokerConfiguration

  func hello() throws {
    let deadline = DispatchTime.now() + .milliseconds(200)
    let socket = try BrokerSocket(port: configuration.port, deadline: deadline)
    defer { socket.close() }
    try socket.write(brokerFrame(["v": 1, "secret": configuration.secret, "hello": true], limit: 512),
                     deadline: deadline)
    guard try socket.read(maximum: 1, deadline: deadline) == Data([72]) else {
      throw FlowBrokerError.invalidReply
    }
  }

  // handleNewFlow must answer synchronously. Use a nonblocking socket with one
  // absolute deadline, without waiting on a callback queue (including main).
  func query(pid: pid_t) throws -> BrokerSocket? {
    guard pid > 1 else { throw FlowBrokerError.invalidFrame }
    let deadline = DispatchTime.now() + .milliseconds(200)
    let socket = try BrokerSocket(port: configuration.port, deadline: deadline)
    do {
      try socket.write(brokerFrame(["v": 1, "pid": pid, "secret": configuration.secret], limit: 512),
                       deadline: deadline)
      switch try socket.read(maximum: 1, deadline: deadline).first {
      case 89: return socket
      case 78: socket.close(); return nil
      default: throw FlowBrokerError.invalidReply
      }
    } catch {
      socket.close()
      throw error
    }
  }
}

final class BrokerSocket {
  private var descriptor: Int32

  init(port: UInt16, deadline: DispatchTime) throws {
    descriptor = Darwin.socket(AF_INET, SOCK_STREAM, IPPROTO_TCP)
    guard descriptor >= 0 else { throw FlowBrokerError.socket(errno) }
    do {
      var enabled: Int32 = 1
      guard fcntl(descriptor, F_SETFL, O_NONBLOCK) == 0,
            setsockopt(descriptor, SOL_SOCKET, SO_NOSIGPIPE, &enabled, 4) == 0 else {
        throw FlowBrokerError.socket(errno)
      }
      var address = sockaddr_in()
      address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
      address.sin_family = sa_family_t(AF_INET)
      address.sin_port = port.bigEndian
      address.sin_addr.s_addr = inet_addr("127.0.0.1")
      let connected = withUnsafePointer(to: &address) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
          Darwin.connect(descriptor, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
      }
      if connected != 0 {
        guard errno == EINPROGRESS else { throw FlowBrokerError.socket(errno) }
        try wait(for: Int16(POLLOUT), deadline: deadline)
        var result: Int32 = 0
        var size = socklen_t(MemoryLayout<Int32>.size)
        guard getsockopt(descriptor, SOL_SOCKET, SO_ERROR, &result, &size) == 0 else {
          throw FlowBrokerError.socket(errno)
        }
        guard result == 0 else { throw FlowBrokerError.socket(result) }
      }
    } catch {
      close()
      throw error
    }
  }

  // Transfers sole ownership to the relay. No concurrent synchronous I/O may
  // remain when this is called.
  func takeDescriptor() -> Int32 {
    let result = descriptor
    descriptor = -1
    return result
  }

  func close() {
    if descriptor >= 0 { Darwin.close(descriptor); descriptor = -1 }
  }

  deinit { close() }

  func write(_ data: Data, deadline: DispatchTime) throws {
    try data.withUnsafeBytes { bytes in
      var offset = 0
      while offset < data.count {
        try wait(for: Int16(POLLOUT), deadline: deadline)
        let count = send(descriptor, bytes.baseAddress!.advanced(by: offset), data.count - offset, 0)
        if count > 0 { offset += count }
        else if count < 0 && (errno == EINTR || errno == EAGAIN) { continue }
        else { throw FlowBrokerError.socket(count == 0 ? EPIPE : errno) }
      }
    }
  }

  func read(maximum: Int, deadline: DispatchTime) throws -> Data {
    var buffer = [UInt8](repeating: 0, count: maximum)
    while true {
      try wait(for: Int16(POLLIN), deadline: deadline)
      let count = recv(descriptor, &buffer, maximum, 0)
      if count > 0 { return Data(buffer.prefix(count)) }
      if count == 0 { throw FlowBrokerError.endOfStream }
      if errno != EINTR && errno != EAGAIN { throw FlowBrokerError.socket(errno) }
    }
  }

  private func wait(for events: Int16, deadline: DispatchTime) throws {
    while true {
      let now = DispatchTime.now().uptimeNanoseconds
      guard now < deadline.uptimeNanoseconds else { throw FlowBrokerError.timedOut }
      let remaining = (deadline.uptimeNanoseconds - now + 999_999) / 1_000_000
      var descriptor = pollfd(fd: self.descriptor, events: events, revents: 0)
      let result = poll(&descriptor, 1, Int32(min(remaining, UInt64(Int32.max))))
      if result > 0 {
        if descriptor.revents & Int16(POLLNVAL) != 0 { throw FlowBrokerError.socket(EBADF) }
        return // Read/write or SO_ERROR reports EOF, reset and other errors.
      }
      if result == 0 { throw FlowBrokerError.timedOut }
      if errno != EINTR { throw FlowBrokerError.socket(errno) }
    }
  }
}

private func brokerFrame(_ fields: [String: Any], limit: Int) throws -> Data {
  let encoded = try JSONSerialization.data(withJSONObject: fields)
  guard !encoded.isEmpty, encoded.count <= limit else { throw FlowBrokerError.invalidFrame }
  return Data([UInt8(encoded.count >> 8), UInt8(encoded.count & 255)]) + encoded
}
