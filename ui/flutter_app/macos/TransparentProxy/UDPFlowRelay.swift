import Darwin
import Foundation

// One UDP datagram and the remote endpoint it is sent to or came from, as
// "ip:port" ("[ip]:port" for IPv6).
struct ProxyDatagram: Equatable {
  let payload: Data
  let endpoint: String
}

protocol ProxyUDPFlow: AnyObject {
  func open(completion: @escaping (Error?) -> Void)
  // nil datagrams mean the application closed the flow.
  func readDatagrams(completion: @escaping ([ProxyDatagram]?, Error?) -> Void)
  func writeDatagrams(_ datagrams: [ProxyDatagram], completion: @escaping (Error?) -> Void)
  func close(error: Error?)
}

func datagramEndpoint(host: String, port: UInt16) -> String {
  host.contains(":") ? "[\(host)]:\(port)" : "\(host):\(port)"
}

// Splits an endpoint written by datagramEndpoint, accepting IP literals only.
func parseDatagramEndpoint(_ endpoint: String) -> (host: String, port: UInt16)? {
  let host: Substring
  let port: Substring
  if endpoint.hasPrefix("[") {
    guard let close = endpoint.firstIndex(of: "]"),
          endpoint[endpoint.index(after: close)...].hasPrefix(":") else { return nil }
    host = endpoint[endpoint.index(after: endpoint.startIndex)..<close]
    port = endpoint[endpoint.index(close, offsetBy: 2)...]
  } else {
    guard let colon = endpoint.lastIndex(of: ":") else { return nil }
    host = endpoint[..<colon]
    port = endpoint[endpoint.index(after: colon)...]
  }
  guard let number = UInt16(port), number > 0 else { return nil }
  let address = String(host.split(separator: "%", maxSplits: 1).first ?? "")
  if endpoint.hasPrefix("[") {
    var ipv6 = in6_addr()
    guard inet_pton(AF_INET6, address, &ipv6) == 1 else { return nil }
  } else {
    var ipv4 = in_addr()
    guard inet_pton(AF_INET, address, &ipv4) == 1 else { return nil }
  }
  return (String(host), number)
}

// The broker connection carries one frame per datagram in each direction: a
// 4-byte big-endian length of the rest, a 1-byte endpoint length, the
// endpoint and the payload. vibermate run reads the same format.
enum DatagramFrame {
  static let maxPayload = 65_535
  static let maxEndpoint = 255
  static let maxFrame = 1 + maxEndpoint + maxPayload

  static func encode(_ datagram: ProxyDatagram) -> Data? {
    let endpoint = Data(datagram.endpoint.utf8)
    guard !endpoint.isEmpty, endpoint.count <= maxEndpoint, datagram.payload.count <= maxPayload,
          parseDatagramEndpoint(datagram.endpoint) != nil else { return nil }
    let length = UInt32(1 + endpoint.count + datagram.payload.count)
    var frame = Data(capacity: 4 + Int(length))
    withUnsafeBytes(of: length.bigEndian) { frame.append(contentsOf: $0) }
    frame.append(UInt8(endpoint.count))
    frame.append(endpoint)
    frame.append(datagram.payload)
    return frame
  }
}

struct DatagramFrameDecoder {
  private var buffer = Data()

  // Returns every complete datagram so far; keeps a partial frame for later.
  mutating func append(_ data: Data) throws -> [ProxyDatagram] {
    buffer.append(data)
    var datagrams: [ProxyDatagram] = []
    while buffer.count >= 4 {
      let start = buffer.startIndex
      let length = buffer[start..<start + 4].reduce(0) { $0 << 8 | Int($1) }
      guard length >= 1, length <= DatagramFrame.maxFrame else { throw FlowBrokerError.invalidFrame }
      guard buffer.count >= 4 + length else { break }
      let frame = buffer[(start + 4)..<(start + 4 + length)]
      let endpointLength = Int(frame[frame.startIndex])
      guard endpointLength >= 1, 1 + endpointLength <= frame.count,
            let endpoint = String(data: frame[(frame.startIndex + 1)..<(frame.startIndex + 1 + endpointLength)],
                                  encoding: .utf8),
            parseDatagramEndpoint(endpoint) != nil else { throw FlowBrokerError.invalidFrame }
      datagrams.append(ProxyDatagram(payload: Data(frame[(frame.startIndex + 1 + endpointLength)...]),
                                     endpoint: endpoint))
      buffer = Data(buffer[(start + 4 + length)...])
    }
    return datagrams
  }
}

final class UDPFlowRelay: FlowRelay {
  private let flow: ProxyUDPFlow
  private let queue = DispatchQueue(label: "io.vibermate.transparent-proxy.udp-relay")
  private var socket: RelayDescriptor?
  private var reader: DispatchSourceRead?
  private var writer: DispatchSourceWrite?
  private var readerSuspended = true
  private var writerSuspended = true
  private var pending: Data?
  private var offset = 0
  private var afterWrite: (() -> Void)?
  private var onFinish: (() -> Void)?
  private var decoder = DatagramFrameDecoder()
  private var started = false
  private var finished = false

  init(flow: ProxyUDPFlow, socket: BrokerSocket, onFinish: @escaping () -> Void) {
    self.flow = flow
    self.socket = RelayDescriptor(socket.takeDescriptor())
    self.onFinish = onFinish
  }

  func start(header: Data) {
    queue.async { [self] in
      guard !self.finished, !self.started, let socket = self.socket else { return }
      self.started = true
      let reader = DispatchSource.makeReadSource(fileDescriptor: socket.value, queue: self.queue)
      let writer = DispatchSource.makeWriteSource(fileDescriptor: socket.value, queue: self.queue)
      self.reader = reader
      self.writer = writer
      reader.setEventHandler { [weak self] in self?.readBroker() }
      writer.setEventHandler { [weak self] in self?.writeBroker() }
      reader.setCancelHandler { withExtendedLifetime(socket) {} }
      writer.setCancelHandler { withExtendedLifetime(socket) {} }
      self.send(header) {
        self.flow.open { [weak self] error in
          guard let self = self else { return }
          self.queue.async {
            guard !self.finished else { return }
            if let error = error { self.finish(error: error); return }
            self.resumeReader()
            self.readClient()
          }
        }
      }
    }
  }

  func stop() {
    queue.async { self.finish(error: NSError(domain: NSPOSIXErrorDomain, code: Int(ECANCELED))) }
  }

  private func send(_ data: Data, completion: @escaping () -> Void) {
    pending = data
    offset = 0
    afterWrite = completion
    if writerSuspended { writerSuspended = false; writer?.resume() }
  }

  private func writeBroker() {
    guard !finished, let socket = socket, let data = pending else { return }
    let count = data.withUnsafeBytes { bytes in
      Darwin.send(socket.value, bytes.baseAddress!.advanced(by: offset),
                  min(data.count - offset, 64 * 1024), 0)
    }
    if count < 0 {
      if errno != EINTR && errno != EAGAIN { failSocket() }
      return
    }
    if count == 0 { finish(error: FlowBrokerError.endOfStream); return }
    offset += count
    if offset == data.count {
      writer?.suspend()
      writerSuspended = true
      pending = nil
      let completion = afterWrite
      afterWrite = nil
      completion?()
    }
  }

  private func readClient() {
    guard !finished else { return }
    flow.readDatagrams { [weak self] datagrams, error in
      guard let self = self else { return }
      self.queue.async {
        guard !self.finished else { return }
        if let error = error { self.finish(error: error); return }
        guard let datagrams = datagrams else { self.finish(error: nil); return }
        var frames = Data()
        for datagram in datagrams {
          // Loopback stays on this Mac, as the capture rules keep it for TCP.
          guard let (host, port) = parseDatagramEndpoint(datagram.endpoint),
                !FlowDestination(host: host, port: port, remoteHostname: nil).isLoopback,
                let frame = DatagramFrame.encode(datagram) else { continue }
          frames.append(frame)
        }
        if frames.isEmpty { self.readClient() } else { self.send(frames) { self.readClient() } }
      }
    }
  }

  private func readBroker() {
    guard !finished, let socket = socket else { return }
    var buffer = [UInt8](repeating: 0, count: 64 * 1024)
    let count = recv(socket.value, &buffer, buffer.count, 0)
    if count < 0 {
      if errno != EINTR && errno != EAGAIN { failSocket() }
      return
    }
    if count == 0 { finish(error: nil); return }
    let datagrams: [ProxyDatagram]
    do { datagrams = try decoder.append(Data(buffer.prefix(count))) }
    catch { finish(error: error); return }
    if datagrams.isEmpty { return }
    reader?.suspend()
    readerSuspended = true
    flow.writeDatagrams(datagrams) { [weak self] error in
      guard let self = self else { return }
      self.queue.async {
        guard !self.finished else { return }
        if let error = error { self.finish(error: error); return }
        self.resumeReader()
      }
    }
  }

  private func resumeReader() {
    if readerSuspended {
      readerSuspended = false
      reader?.resume()
    }
  }

  private func failSocket() {
    finish(error: NSError(domain: NSPOSIXErrorDomain, code: Int(errno)))
  }

  private func finish(error: Error?) {
    guard !finished else { return }
    finished = true
    flow.close(error: error)
    if let socket = socket { shutdown(socket.value, SHUT_RDWR) }
    if readerSuspended { reader?.resume() }
    if writerSuspended { writer?.resume() }
    reader?.cancel()
    writer?.cancel()
    reader = nil
    writer = nil
    socket = nil
    pending = nil
    afterWrite = nil
    let completion = onFinish
    onFinish = nil
    completion?()
  }
}
