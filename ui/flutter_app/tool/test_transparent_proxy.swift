import Darwin
import Foundation
import Network
import NetworkExtension
import XCTest

private let testSecret = String(repeating: "a", count: 43)

@objcMembers
final class TransparentProxyTests: XCTestCase {
  func testLaunchOptionsRejectInvalidPortsAndUnframeableSecrets() throws {
    let valid = try FlowBrokerConfiguration(options: [
      "flowPort": NSNumber(value: 49152), "flowSecret": testSecret as NSString,
    ])
    XCTAssertEqual(valid.port, 49152)
    for invalid: Any in [true, 0, -1, 65536, 1.5, "49152"] {
      XCTAssertThrowsError(try FlowBrokerConfiguration(options: [
        "flowPort": invalid as AnyObject, "flowSecret": testSecret as NSString,
      ]))
    }
    for invalid in ["", "short", String(repeating: "a", count: 600),
                    String(repeating: "\u{1}", count: 100)] {
      XCTAssertThrowsError(try FlowBrokerConfiguration(options: [
        "flowPort": 49152 as NSNumber, "flowSecret": invalid as NSString,
      ]))
    }
    XCTAssertThrowsError(try FlowBrokerConfiguration(options: nil))
  }

  func testHelloUsesLengthPrefixAndAuthenticatedProtocol() throws {
    let server = try LoopbackBroker { socket in
      let request = try readFrame(socket, limit: 512)
      XCTAssertEqual(request["v"] as? Int, 1)
      XCTAssertEqual(request["secret"] as? String, testSecret)
      XCTAssertEqual(request["hello"] as? Bool, true)
      XCTAssertNil(request["pid"])
      try sendAll(socket, Data([72]))
    }
    try FlowBroker(configuration: server.configuration()).hello()
    server.finish()
  }

  func testLivenessStreamStaysOpenUntilTheExtensionCloses() throws {
    let observedClose = DispatchSemaphore(value: 0)
    let server = try LoopbackBroker { socket in
      let request = try readFrame(socket, limit: 512)
      XCTAssertEqual(request["hello"] as? Bool, true)
      XCTAssertEqual(request["stream"] as? Bool, true)
      XCTAssertEqual(request["queryFailures"] as? Int, 4)
      XCTAssertNil(request["pid"])
      try sendAll(socket, Data([72]))
      // The broker sends nothing more; it learns of the close as EOF.
      XCTAssertThrowsError(try readExactly(socket, count: 1))
      observedClose.signal()
    }
    let stream = try FlowBroker(configuration: server.configuration()).openLivenessStream(queryFailures: 4)
    XCTAssertFalse(stream.peerClosed())
    stream.close()
    XCTAssertEqual(observedClose.wait(timeout: .now() + .seconds(3)), .success)
    server.finish()
  }

  func testLivenessStreamNoticesTheBrokerGoingAway() throws {
    let server = try LoopbackBroker { socket in
      _ = try readFrame(socket, limit: 512)
      try sendAll(socket, Data([72]))
    }
    let stream = try FlowBroker(configuration: server.configuration()).openLivenessStream(queryFailures: 0)
    server.finish() // The fixture closed its end.
    let deadline = Date().addingTimeInterval(2)
    while !stream.peerClosed() && Date() < deadline { usleep(10_000) }
    XCTAssertTrue(stream.peerClosed())
    stream.close()
  }

  func testPeriodicHelloOmitsStreamAndReportsFailures() throws {
    let server = try LoopbackBroker { socket in
      let request = try readFrame(socket, limit: 512)
      XCTAssertNil(request["stream"])
      XCTAssertEqual(request["queryFailures"] as? Int, 9)
      try sendAll(socket, Data([72]))
    }
    try FlowBroker(configuration: server.configuration()).hello(queryFailures: 9)
    server.finish()
  }

  func testOwnedQueryKeepsSameSocketForHeaderAndPayload() throws {
    let server = try LoopbackBroker { socket in
      let request = try readFrame(socket, limit: 512)
      XCTAssertEqual(request["v"] as? Int, 1)
      XCTAssertEqual(request["pid"] as? Int, 8123)
      XCTAssertEqual(request["secret"] as? String, testSecret)
      XCTAssertNil(request["hello"])
      try sendAll(socket, Data([89]))
      let header = try readFrame(socket, limit: 1024)
      XCTAssertEqual(header["v"] as? Int, 1)
      XCTAssertEqual(header["proto"] as? String, "tcp")
      XCTAssertEqual(header["dst"] as? String, "[2001:db8::7]:443")
      XCTAssertEqual(header["host"] as? String, "api.example.com")
      XCTAssertNil(header["secret"])
      XCTAssertEqual(try readExactly(socket, count: 4), Data("ping".utf8))
      try sendAll(socket, Data("pong".utf8))
    }
    let broker = FlowBroker(configuration: try server.configuration())
    let socket = try XCTUnwrap(broker.query(pid: 8123))
    defer { socket.close() }
    let header = try FlowDestination(host: "2001:db8::7", port: 443,
                                     remoteHostname: "api.example.com").header()
    try socket.write(header + Data("ping".utf8), deadline: .now() + .seconds(1))
    XCTAssertEqual(try socket.read(maximum: 4, deadline: .now() + .seconds(1)),
                   Data("pong".utf8))
    server.finish()
  }

  func testUnownedReplyDeclinesButInvalidReplyAndEOFThrow() throws {
    for reply: Data in [Data([78]), Data([72]), Data()] {
      let server = try LoopbackBroker { socket in
        _ = try readFrame(socket, limit: 512)
        if !reply.isEmpty { try sendAll(socket, reply) }
      }
      let broker = FlowBroker(configuration: try server.configuration())
      if reply == Data([78]) {
        XCTAssertNil(try broker.query(pid: 8123))
      } else {
        XCTAssertThrowsError(try broker.query(pid: 8123))
      }
      server.finish()
    }
  }

  func testQueryDeadlineIncludesWaitingForBrokerResponse() throws {
    let server = try LoopbackBroker { socket in
      _ = try readFrame(socket, limit: 512)
      Thread.sleep(forTimeInterval: 0.35)
    }
    let start = DispatchTime.now().uptimeNanoseconds
    XCTAssertThrowsError(try FlowBroker(configuration: server.configuration()).query(pid: 8123))
    let elapsed = Double(DispatchTime.now().uptimeNanoseconds - start) / 1_000_000_000
    XCTAssertGreaterThan(elapsed, 0.1)
    XCTAssertLessThan(elapsed, 0.32)
    server.finish()
  }

  func testDestinationExcludesLoopbackAndDoesNotInventHostname() throws {
    for host in ["127.0.0.1", "127.255.0.2", "::1", "::ffff:127.0.0.1", "localhost"] {
      XCTAssertTrue(FlowDestination(host: host, port: 443, remoteHostname: nil).isLoopback)
    }
    for host in ["", "128.0.0.1", "2001:db8::7", "api.example.com"] {
      XCTAssertFalse(FlowDestination(host: host, port: 443, remoteHostname: nil).isLoopback)
    }
    let frame = try FlowDestination(host: "203.0.113.7", port: 443, remoteHostname: "").header()
    let header = try XCTUnwrap(JSONSerialization.jsonObject(with: frame.dropFirst(2)) as? [String: Any])
    XCTAssertEqual(header["dst"] as? String, "203.0.113.7:443")
    XCTAssertNil(header["host"])
    XCTAssertThrowsError(try FlowDestination(host: "203.0.113.7", port: 443,
      remoteHostname: String(repeating: "h", count: 1024)).header())
  }

  func testRelayDeliversLargePayloadAndResponseAfterClientHalfClose() throws {
    let request = Data((0..<600_000).map { UInt8($0 % 251) })
    let response = Data((0..<180_000).map { UInt8($0 % 239) })
    let server = try LoopbackBroker { socket in
      _ = try readFrame(socket, limit: 512)
      try sendAll(socket, Data([89]))
      _ = try readFrame(socket, limit: 1024)
      XCTAssertEqual(try readExactly(socket, count: request.count), request)
      var byte: UInt8 = 0
      XCTAssertEqual(recv(socket, &byte, 1, 0), 0)
      try sendAll(socket, response)
      shutdown(socket, SHUT_WR)
    }
    let broker = FlowBroker(configuration: try server.configuration())
    let socket = try XCTUnwrap(broker.query(pid: 8123))
    let (flow, appSocket) = try SocketProxyFlow.makePair()
    defer { Darwin.close(appSocket) }
    let finished = DispatchSemaphore(value: 0)
    let relay = TCPFlowRelay(flow: flow, socket: socket, onFinish: { finished.signal() })
    relay.start(header: try FlowDestination(host: "203.0.113.7", port: 443, remoteHostname: nil).header())
    try sendAll(appSocket, request)
    shutdown(appSocket, SHUT_WR)
    XCTAssertEqual(try readExactly(appSocket, count: response.count), response)
    var byte: UInt8 = 0
    XCTAssertEqual(recv(appSocket, &byte, 1, 0), 0)
    XCTAssertEqual(finished.wait(timeout: .now() + .seconds(2)), .success)
    server.finish()
  }

  func testRelayKeepsClientDirectionOpenAfterBrokerHalfClose() throws {
    let server = try LoopbackBroker { socket in
      _ = try readFrame(socket, limit: 512)
      try sendAll(socket, Data([89]))
      _ = try readFrame(socket, limit: 1024)
      try sendAll(socket, Data("response".utf8))
      shutdown(socket, SHUT_WR)
      XCTAssertEqual(try readExactly(socket, count: 7), Data("request".utf8))
      var byte: UInt8 = 0
      XCTAssertEqual(recv(socket, &byte, 1, 0), 0)
    }
    let socket = try XCTUnwrap(FlowBroker(configuration: server.configuration()).query(pid: 8123))
    let (flow, appSocket) = try SocketProxyFlow.makePair()
    defer { Darwin.close(appSocket) }
    let finished = DispatchSemaphore(value: 0)
    let relay = TCPFlowRelay(flow: flow, socket: socket, onFinish: { finished.signal() })
    relay.start(header: try FlowDestination(host: "203.0.113.7", port: 443, remoteHostname: nil).header())
    XCTAssertEqual(try readExactly(appSocket, count: 8), Data("response".utf8))
    var byte: UInt8 = 0
    XCTAssertEqual(recv(appSocket, &byte, 1, 0), 0)
    try sendAll(appSocket, Data("request".utf8))
    shutdown(appSocket, SHUT_WR)
    XCTAssertEqual(finished.wait(timeout: .now() + .seconds(2)), .success)
    server.finish()
  }

  func testAuditIdentityRejectsMalformedTokenAndDistinguishesPIDReuse() throws {
    var token = audit_token_t()
    token.val = (0, 0, 0, 0, 0, 8123, 0, 91)
    let bytes = withUnsafeBytes(of: token) { Data($0) }
    let identity = try XCTUnwrap(FlowSourceIdentity(auditToken: bytes))
    XCTAssertEqual(identity.pid, 8123)
    XCTAssertEqual(identity.pidVersion, 91)
    XCTAssertNil(FlowSourceIdentity(auditToken: nil))
    XCTAssertNil(FlowSourceIdentity(auditToken: bytes.dropLast()))
    XCTAssertNil(FlowSourceIdentity(auditToken: bytes + Data([0])))
    token.val.5 = 1
    XCTAssertNil(FlowSourceIdentity(auditToken: withUnsafeBytes(of: token) { Data($0) }))
    var history = FlowOwnershipHistory()
    history.recordOwned(identity)
    XCTAssertTrue(history.contains(identity))
    token.val.5 = 8123
    token.val.7 = 92
    let reused = try XCTUnwrap(FlowSourceIdentity(auditToken: withUnsafeBytes(of: token) { Data($0) }))
    XCTAssertFalse(history.contains(reused))
    history.recordUnowned(identity)
    XCTAssertFalse(history.contains(identity))
  }

  func testRulesCaptureOnlyOutboundTCPAndExcludeLoopback() throws {
    let settings = TransparentProxyProvider.networkSettings()
    let included = try XCTUnwrap(settings.includedNetworkRules)
    XCTAssertEqual(included.count, 1)
    XCTAssertEqual(included[0].matchProtocol, .TCP)
    XCTAssertEqual(included[0].matchDirection, .outbound)
    let excluded = try XCTUnwrap(settings.excludedNetworkRules)
    XCTAssertEqual(excluded.count, 2)
    XCTAssertEqual(excluded.map(\.matchRemotePrefix), [8, 128])
    for rule in included + excluded {
      XCTAssertEqual(rule.matchProtocol, .TCP)
      XCTAssertEqual(rule.matchDirection, .outbound)
      #if compiler(>=6.0)
      if #available(macOS 15.0, *) {
        XCTAssertNil(rule.matchLocalNetworkEndpoint)
        continue
      }
      #endif
      XCTAssertNil(rule.matchLocalNetwork)
    }
    #if compiler(>=6.0)
    if #available(macOS 15.0, *) {
      XCTAssertNil(included[0].matchRemoteHostOrNetworkEndpoint)
      XCTAssertEqual(excluded[0].matchRemoteHostOrNetworkEndpoint, .hostPort(host: "127.0.0.0", port: 0))
      XCTAssertEqual(excluded[1].matchRemoteHostOrNetworkEndpoint, .hostPort(host: "::1", port: 0))
    }
    #endif
  }

  func testRelayStopClosesBothPeersOnceEvenIfStartIsPending() throws {
    for startFirst in [false, true] {
      let server = try LoopbackBroker { socket in
        _ = try readFrame(socket, limit: 512)
        try sendAll(socket, Data([89]))
        var buffer = [UInt8](repeating: 0, count: 2048)
        while true {
          let count = recv(socket, &buffer, buffer.count, 0)
          if count == 0 { return }
          if count < 0 { throw TestSocketError.failed(errno) }
        }
      }
      let socket = try XCTUnwrap(FlowBroker(configuration: server.configuration()).query(pid: 8123))
      let (flow, appSocket) = try SocketProxyFlow.makePair()
      defer { Darwin.close(appSocket) }
      let finished = DispatchSemaphore(value: 0)
      let relay = TCPFlowRelay(flow: flow, socket: socket, onFinish: { finished.signal() })
      let header = try FlowDestination(host: "203.0.113.7", port: 443, remoteHostname: nil).header()
      if startFirst { relay.start(header: header) }
      relay.stop()
      relay.stop()
      if !startFirst { relay.start(header: header) }
      var byte: UInt8 = 0
      XCTAssertEqual(recv(appSocket, &byte, 1, 0), 0)
      XCTAssertEqual(finished.wait(timeout: .now() + .seconds(2)), .success)
      XCTAssertEqual(finished.wait(timeout: .now() + .milliseconds(20)), .timedOut)
      server.finish()
    }
  }
}

private enum TestSocketError: Error { case failed(Int32), eof, invalidFrame }

// The Network Extension creates NEAppProxyTCPFlow instances. A socket pair
// exercises the same async read/write boundary here without activating it.
private final class SocketProxyFlow: ProxyTCPFlow {
  private let socket: Int32
  private init(socket: Int32) { self.socket = socket }

  static func makePair() throws -> (SocketProxyFlow, Int32) {
    var pair: [Int32] = [-1, -1]
    guard socketpair(AF_UNIX, SOCK_STREAM, 0, &pair) == 0 else { throw TestSocketError.failed(errno) }
    for socket in pair {
      var timeout = timeval(tv_sec: 2, tv_usec: 0)
      _ = setsockopt(socket, SOL_SOCKET, SO_RCVTIMEO, &timeout, socklen_t(MemoryLayout.size(ofValue: timeout)))
      _ = setsockopt(socket, SOL_SOCKET, SO_SNDTIMEO, &timeout, socklen_t(MemoryLayout.size(ofValue: timeout)))
      var noSignal: Int32 = 1
      _ = setsockopt(socket, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, 4)
    }
    return (SocketProxyFlow(socket: pair[0]), pair[1])
  }

  func open(completion: @escaping (Error?) -> Void) { completion(nil) }

  func read(completion: @escaping (Data?, Error?) -> Void) {
    DispatchQueue.global().async {
      var buffer = [UInt8](repeating: 0, count: 16 * 1024)
      let count = recv(self.socket, &buffer, buffer.count, 0)
      if count < 0 { completion(nil, TestSocketError.failed(errno)) }
      else { completion(Data(buffer.prefix(count)), nil) }
    }
  }

  func write(_ data: Data, completion: @escaping (Error?) -> Void) {
    DispatchQueue.global().async {
      do { try sendAll(self.socket, data); completion(nil) }
      catch { completion(error) }
    }
  }

  func closeRead(error: Error?) { shutdown(socket, SHUT_RD) }
  func closeWrite(error: Error?) { shutdown(socket, SHUT_WR) }
  deinit { Darwin.close(socket) }
}

private final class LoopbackBroker {
  let listener: Int32
  let port: UInt16
  private let done = DispatchSemaphore(value: 0)

  init(handler: @escaping (Int32) throws -> Void) throws {
    let listener = Darwin.socket(AF_INET, SOCK_STREAM, 0)
    self.listener = listener
    guard listener >= 0 else { throw TestSocketError.failed(errno) }
    var address = sockaddr_in()
    address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
    address.sin_family = sa_family_t(AF_INET)
    address.sin_addr.s_addr = inet_addr("127.0.0.1")
    let bound = withUnsafePointer(to: &address) {
      $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
        Darwin.bind(listener, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
      }
    }
    guard bound == 0, listen(listener, 1) == 0 else {
      Darwin.close(listener)
      throw TestSocketError.failed(errno)
    }
    var length = socklen_t(MemoryLayout<sockaddr_in>.size)
    _ = withUnsafeMutablePointer(to: &address) {
      $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(listener, $0, &length) }
    }
    port = UInt16(bigEndian: address.sin_port)
    let done = self.done
    DispatchQueue.global().async {
      defer { done.signal() }
      var event = pollfd(fd: listener, events: Int16(POLLIN), revents: 0)
      guard poll(&event, 1, 2000) > 0 else { XCTFail("broker was never contacted"); return }
      let socket = accept(listener, nil, nil)
      guard socket >= 0 else { XCTFail("accept failed"); return }
      defer { Darwin.close(socket) }
      var timeout = timeval(tv_sec: 2, tv_usec: 0)
      _ = setsockopt(socket, SOL_SOCKET, SO_RCVTIMEO, &timeout, socklen_t(MemoryLayout.size(ofValue: timeout)))
      var noSignal: Int32 = 1
      _ = setsockopt(socket, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, 4)
      do { try handler(socket) } catch { XCTFail("broker fixture failed: \(error)") }
    }
  }

  func configuration() throws -> FlowBrokerConfiguration {
    try FlowBrokerConfiguration(options: ["flowPort": NSNumber(value: port), "flowSecret": testSecret as NSString])
  }

  func finish() {
    XCTAssertEqual(done.wait(timeout: .now() + .seconds(3)), .success)
  }

  deinit { Darwin.close(listener) }
}

private func readExactly(_ socket: Int32, count: Int) throws -> Data {
  var data = Data(count: count)
  var offset = 0
  try data.withUnsafeMutableBytes { bytes in
    while offset < count {
      let received = recv(socket, bytes.baseAddress!.advanced(by: offset), count - offset, 0)
      guard received > 0 else { throw TestSocketError.eof }
      offset += received
    }
  }
  return data
}

private func readFrame(_ socket: Int32, limit: Int) throws -> [String: Any] {
  let prefix = try readExactly(socket, count: 2)
  let length = Int(prefix[0]) << 8 | Int(prefix[1])
  guard length > 0, length <= limit,
        let object = try JSONSerialization.jsonObject(with: readExactly(socket, count: length)) as? [String: Any]
  else { throw TestSocketError.invalidFrame }
  return object
}

private func sendAll(_ socket: Int32, _ data: Data) throws {
  try data.withUnsafeBytes { bytes in
    var offset = 0
    while offset < data.count {
      let sent = send(socket, bytes.baseAddress!.advanced(by: offset), data.count - offset, 0)
      guard sent > 0 else { throw TestSocketError.failed(errno) }
      offset += sent
    }
  }
}

@main
enum TransparentProxyTestRunner {
  static func main() {
    let suite = XCTestSuite(forTestCaseClass: TransparentProxyTests.self)
    suite.run()
    guard let result = suite.testRun else { exit(1) }
    print("Transparent proxy: \(result.executionCount) tests, \(result.totalFailureCount) failures")
    exit(result.executionCount > 0 && result.totalFailureCount == 0 ? 0 : 1)
  }
}
