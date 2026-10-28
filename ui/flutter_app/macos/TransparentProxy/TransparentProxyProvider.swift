import Darwin
import Foundation
import Network
import NetworkExtension

final class TransparentProxyProvider: NETransparentProxyProvider {
  private let sessionLock = NSLock()
  private var session: ProxySession?

  override func startProxy(options: [String: Any]?, completionHandler: @escaping (Error?) -> Void) {
    let configuration: FlowBrokerConfiguration
    do {
      configuration = try FlowBrokerConfiguration(options: options?.mapValues { $0 as AnyObject })
    } catch {
      completionHandler(error)
      return
    }
    let current = ProxySession(configuration: configuration)
    sessionLock.lock()
    let previous = session
    session = current
    sessionLock.unlock()
    previous?.stop()

    self.setTunnelNetworkSettings(Self.networkSettings()) { error in
      if let error = error {
        self.startFailed(current, error: error, completion: completionHandler)
        return
      }
      current.livenessQueue.async {
        guard self.isCurrent(current) else {
          self.startFailed(current, error: FlowBrokerError.endOfStream, completion: completionHandler)
          return
        }
        // Announce readiness only after capture rules are installed, so a run
        // cannot observe a healthy hello and launch before interception starts.
        do { try current.openLivenessStream() }
        catch { self.startFailed(current, error: error, completion: completionHandler); return }
        guard self.isCurrent(current) else {
          self.startFailed(current, error: FlowBrokerError.endOfStream, completion: completionHandler)
          return
        }
        current.startHeartbeat()
        completionHandler(nil)
      }
    }
  }

  override func stopProxy(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
    sessionLock.lock()
    let current = session
    session = nil
    sessionLock.unlock()
    if let current = current { current.stop(completion: completionHandler) }
    else { completionHandler() }
  }

  override func handleNewFlow(_ flow: NEAppProxyFlow) -> Bool {
    guard let identity = FlowSourceIdentity(auditToken: flow.metaData.sourceAppAuditToken) else { return false }
    sessionLock.lock()
    let current = session
    sessionLock.unlock()
    guard let current = current else { return false }
    if let tcp = flow as? NEAppProxyTCPFlow { return handleTCP(tcp, identity: identity, current: current) }
    if let udp = flow as? NEAppProxyUDPFlow { return handleUDP(udp, identity: identity, current: current) }
    return false
  }

  // DNS stays on the system path (ADR 0025): the system resolver's queries
  // reach the extension as the Agent's, and through a server Runtime they
  // would go to a resolver that exists only on this Mac.
  static func staysOnSystemPath(port: UInt16) -> Bool { port == 53 }

  // macOS 14 delivers a UDP flow here with its first destination.
  override func handleNewUDPFlow(_ flow: NEAppProxyUDPFlow,
                                 initialRemoteEndpoint remoteEndpoint: LegacyNetworkEndpoint) -> Bool {
    if let endpoint = remoteEndpoint as? NWHostEndpoint, let port = UInt16(endpoint.port),
       Self.staysOnSystemPath(port: port) {
      return false
    }
    return handleNewFlow(flow)
  }

  private func handleTCP(_ tcp: NEAppProxyTCPFlow, identity: FlowSourceIdentity, current: ProxySession) -> Bool {
    guard let destination = Self.destination(tcp) else {
      return current.wasOwned(identity) ? Self.refuse(tcp) : false
    }
    if destination.isLoopback || Self.staysOnSystemPath(port: destination.port) { return false }
    // An IP literal lets the broker apply the run's address rules; a name is
    // left to the run's ingress.
    let endpoint = datagramEndpoint(host: destination.host, port: destination.port)
    let address = parseDatagramEndpoint(endpoint) != nil ? endpoint : nil
    return claim(tcp, identity: identity, current: current, destination: address) { socket in
      current.add(tcp, socket: socket, header: try destination.header())
    }
  }

  // An owned UDP socket's datagrams are carried through vibermated too, never
  // around it; each datagram names its own destination.
  private func handleUDP(_ udp: NEAppProxyUDPFlow, identity: FlowSourceIdentity, current: ProxySession) -> Bool {
    claim(udp, identity: identity, current: current) { socket in
      current.add(udp, socket: socket, header: try FlowDestination.udpHeader())
    }
  }

  // Asks the broker who owns the flow's process. Owned flows are handed to
  // relay; unowned ones go direct.
  private func claim(_ flow: NEAppProxyFlow, identity: FlowSourceIdentity, current: ProxySession,
                     destination: String? = nil, relay: (BrokerSocket) throws -> Bool) -> Bool {
    do {
      let socket: BrokerSocket
      switch try current.broker.ask(pid: identity.pid, destination: destination) {
      case .unowned:
        current.recordUnowned(identity)
        return false
      case .direct:
        // Still the run's process: keep it known as owned for failed queries.
        current.recordOwned(identity)
        return false
      case .owned(let owned):
        socket = owned
      }
      current.recordOwned(identity)
      do {
        if try !relay(socket) { return Self.refuse(flow) }
      } catch {
        socket.close()
        return Self.refuse(flow)
      }
      return true
    } catch {
      // A failed query cannot tell a first-seen owned process from an
      // unrelated one. Known owned identities are refused; unknown ones go
      // direct so a slow or absent vibermated never cuts off the Mac. Runs
      // pause every process carrying their token whenever the broker or this
      // extension is unavailable, so only a single failed query for a
      // never-seen process of a healthy run remains; it is counted and
      // reported in Host status (ADR 0024).
      current.recordQueryFailure()
      return current.wasOwned(identity) ? Self.refuse(flow) : false
    }
  }

  private func isCurrent(_ current: ProxySession) -> Bool {
    sessionLock.lock()
    defer { sessionLock.unlock() }
    return session === current
  }

  private func startFailed(_ current: ProxySession, error: Error, completion: @escaping (Error?) -> Void) {
    sessionLock.lock()
    if session === current { session = nil }
    sessionLock.unlock()
    current.stop { completion(error) }
  }

  private static func refuse(_ flow: NEAppProxyFlow) -> Bool {
    let error = NSError(domain: NEAppProxyErrorDomain, code: NEAppProxyFlowError.refused.rawValue)
    flow.closeReadWithError(error)
    flow.closeWriteWithError(error)
    return true
  }

  private static func destination(_ flow: NEAppProxyTCPFlow) -> FlowDestination? {
    // Xcode 15's SDK does not declare the macOS 15 endpoint APIs.
    #if compiler(>=6.0)
    if #available(macOS 15.0, *) {
      guard case let .hostPort(host, port) = flow.remoteFlowEndpoint else { return nil }
      return FlowDestination(host: String(describing: host), port: port.rawValue, remoteHostname: flow.remoteHostname)
    }
    #endif
    guard let endpoint = flow.remoteEndpoint as? NWHostEndpoint,
          let port = UInt16(endpoint.port), port > 0 else { return nil }
    return FlowDestination(host: endpoint.hostname, port: port, remoteHostname: flow.remoteHostname)
  }

  static func networkSettings() -> NETransparentProxyNetworkSettings {
    let settings = NETransparentProxyNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
    // nil endpoints match all outbound TCP and UDP. Wildcard IP + port 0 is not a
    // valid transparent-proxy rule. The /8 also excludes the broker socket.
    let protocols: [NENetworkRule.`Protocol`] = [.TCP, .UDP]
    settings.includedNetworkRules = protocols.map { rule(host: nil, prefix: 0, protocol: $0) }
    settings.excludedNetworkRules = protocols.flatMap { matched in
      [rule(host: "127.0.0.0", prefix: 8, protocol: matched), rule(host: "::1", prefix: 128, protocol: matched)]
    }
    return settings
  }

  private static func rule(host: String?, prefix: Int, protocol matched: NENetworkRule.`Protocol`) -> NENetworkRule {
    #if compiler(>=6.0)
    if #available(macOS 15.0, *) {
      let endpoint = host.map { Network.NWEndpoint.hostPort(host: .init($0), port: 0) }
      return NENetworkRule(remoteNetworkEndpoint: endpoint, remotePrefix: prefix,
                           localNetworkEndpoint: nil, localPrefix: 0, protocol: matched, direction: .outbound)
    }
    #endif
    return NENetworkRule(remoteNetwork: host.map { NWHostEndpoint(hostname: $0, port: "0") },
                         remotePrefix: prefix, localNetwork: nil, localPrefix: 0,
                         protocol: matched, direction: .outbound)
  }
}

// macOS 15 and later deliver a UDP flow through this protocol instead.
#if compiler(>=6.0)
@available(macOS 15.0, *)
extension TransparentProxyProvider: NEAppProxyUDPFlowHandling {
  func handleNewUDPFlow(_ flow: NEAppProxyUDPFlow, initialRemoteFlowEndpoint remoteEndpoint: Network.NWEndpoint) -> Bool {
    if case let .hostPort(_, port) = remoteEndpoint, Self.staysOnSystemPath(port: port.rawValue) {
      return false
    }
    return handleNewFlow(flow)
  }
}
#endif

struct FlowSourceIdentity: Hashable {
  let pid: pid_t
  let pidVersion: Int32

  init?(auditToken: Data?) {
    guard let bytes = auditToken, bytes.count == MemoryLayout<audit_token_t>.size else { return nil }
    // NSData storage is not guaranteed to be aligned for audit_token_t.
    var token = audit_token_t()
    _ = withUnsafeMutableBytes(of: &token) { bytes.copyBytes(to: $0) }
    let pid = audit_token_to_pid(token)
    guard pid > 1 else { return nil }
    self.pid = pid
    self.pidVersion = audit_token_to_pidversion(token)
  }
}

struct FlowOwnershipHistory {
  private var known: [pid_t: (version: Int32, order: UInt64)] = [:]
  private var order: UInt64 = 0

  mutating func recordOwned(_ identity: FlowSourceIdentity) {
    order &+= 1
    known[identity.pid] = (identity.pidVersion, order)
    if known.count > 4096, let oldest = known.min(by: { $0.value.order < $1.value.order }) {
      known.removeValue(forKey: oldest.key)
    }
  }

  mutating func recordUnowned(_ identity: FlowSourceIdentity) {
    if contains(identity) { known.removeValue(forKey: identity.pid) }
  }

  func contains(_ identity: FlowSourceIdentity) -> Bool { known[identity.pid]?.version == identity.pidVersion }
}

private final class ProxySession {
  let broker: FlowBroker
  let livenessQueue = DispatchQueue(label: "io.vibermate.transparent-proxy.liveness")
  private let lock = NSLock()
  private let relayGroup = DispatchGroup()
  private var timer: DispatchSourceTimer?
  private var relays: [UUID: FlowRelay] = [:]
  private var history = FlowOwnershipHistory()
  private var stopped = false
  private var stream: BrokerSocket?
  private var queryFailures: UInt64 = 0

  init(configuration: FlowBrokerConfiguration) { broker = FlowBroker(configuration: configuration) }

  func startHeartbeat() {
    lock.lock()
    defer { lock.unlock() }
    guard !stopped, timer == nil else { return }
    let timer = DispatchSource.makeTimerSource(queue: livenessQueue)
    timer.schedule(deadline: .now() + .seconds(10), repeating: .seconds(10), leeway: .milliseconds(100))
    timer.setEventHandler { [weak self] in
      // Keep capture rules and known ownership during an outage. Cancelling
      // the provider here would send all flows directly around the broker.
      guard let self = self else { return }
      try? self.broker.hello(queryFailures: self.failures())
      self.reopenStreamIfClosed()
    }
    self.timer = timer
    timer.resume()
  }

  // Runs on livenessQueue only.
  func openLivenessStream() throws {
    let socket = try broker.openLivenessStream(queryFailures: failures())
    lock.lock()
    guard !stopped else { lock.unlock(); socket.close(); throw FlowBrokerError.endOfStream }
    let previous = stream
    stream = socket
    lock.unlock()
    previous?.close()
  }

  private func reopenStreamIfClosed() {
    lock.lock()
    let current = stream
    let stopped = self.stopped
    lock.unlock()
    guard !stopped, current?.peerClosed() ?? true else { return }
    try? openLivenessStream()
  }

  func recordQueryFailure() {
    lock.lock()
    queryFailures &+= 1
    lock.unlock()
  }

  private func failures() -> UInt64 {
    lock.lock()
    defer { lock.unlock() }
    return queryFailures
  }

  func stop(completion: @escaping () -> Void = {}) {
    lock.lock()
    stopped = true
    let timer = self.timer
    self.timer = nil
    let stream = self.stream
    self.stream = nil
    let active = Array(relays.values)
    lock.unlock()
    timer?.cancel()
    // Closing the stream tells the broker at once; runs pause their Agents.
    // Stream I/O stays on livenessQueue, serialized with the timer's checks.
    livenessQueue.async { stream?.close() }
    for relay in active { relay.stop() }
    relayGroup.notify(queue: livenessQueue, execute: completion)
  }

  func wasOwned(_ identity: FlowSourceIdentity) -> Bool {
    lock.lock()
    defer { lock.unlock() }
    return history.contains(identity)
  }

  func recordOwned(_ identity: FlowSourceIdentity) {
    lock.lock()
    history.recordOwned(identity)
    lock.unlock()
  }

  func recordUnowned(_ identity: FlowSourceIdentity) {
    lock.lock()
    history.recordUnowned(identity)
    lock.unlock()
  }

  func add(_ flow: NEAppProxyTCPFlow, socket: BrokerSocket, header: Data) -> Bool {
    add(socket: socket, header: header) { TCPFlowRelay(flow: SystemTCPFlow(flow), socket: socket, onFinish: $0) }
  }

  func add(_ flow: NEAppProxyUDPFlow, socket: BrokerSocket, header: Data) -> Bool {
    add(socket: socket, header: header) { UDPFlowRelay(flow: SystemUDPFlow(flow), socket: socket, onFinish: $0) }
  }

  private func add(socket: BrokerSocket, header: Data, make: (@escaping () -> Void) -> FlowRelay) -> Bool {
    lock.lock()
    guard !stopped else { lock.unlock(); socket.close(); return false }
    let id = UUID()
    let group = relayGroup
    group.enter()
    let relay = make { [weak self] in
      self?.remove(id)
      group.leave()
    }
    relays[id] = relay
    lock.unlock()
    relay.start(header: header)
    return true
  }

  private func remove(_ id: UUID) {
    lock.lock()
    relays.removeValue(forKey: id)
    lock.unlock()
  }
}

private final class SystemTCPFlow: ProxyTCPFlow {
  private let flow: NEAppProxyTCPFlow
  init(_ flow: NEAppProxyTCPFlow) { self.flow = flow }

  func open(completion: @escaping (Error?) -> Void) {
    #if compiler(>=6.0)
    if #available(macOS 15.0, *) {
      flow.open(withLocalFlowEndpoint: nil, completionHandler: completion)
      return
    }
    #endif
    flow.open(withLocalEndpoint: nil, completionHandler: completion)
  }

  func read(completion: @escaping (Data?, Error?) -> Void) { flow.readData(completionHandler: completion) }
  func write(_ data: Data, completion: @escaping (Error?) -> Void) { flow.write(data, withCompletionHandler: completion) }
  func closeRead(error: Error?) { flow.closeReadWithError(flowError(error)) }
  func closeWrite(error: Error?) { flow.closeWriteWithError(flowError(error)) }

  private func flowError(_ error: Error?) -> Error? {
    guard let error = error as NSError? else { return nil }
    if error.domain == NEAppProxyErrorDomain { return error }
    return NSError(domain: NEAppProxyErrorDomain, code: NEAppProxyFlowError.internal.rawValue,
                   userInfo: [NSUnderlyingErrorKey: error])
  }
}

private final class SystemUDPFlow: ProxyUDPFlow {
  private let flow: NEAppProxyUDPFlow
  init(_ flow: NEAppProxyUDPFlow) { self.flow = flow }

  func open(completion: @escaping (Error?) -> Void) {
    #if compiler(>=6.0)
    if #available(macOS 15.0, *) {
      flow.open(withLocalFlowEndpoint: nil, completionHandler: completion)
      return
    }
    #endif
    flow.open(withLocalEndpoint: nil, completionHandler: completion)
  }

  // An empty or missing read means the application closed the flow.
  // Datagrams with an endpoint that is not an IP address and port are dropped.
  func readDatagrams(completion: @escaping ([ProxyDatagram]?, Error?) -> Void) {
    #if compiler(>=6.0)
    if #available(macOS 15.0, *) {
      flow.readDatagrams { pairs, error in
        guard let pairs = pairs, !pairs.isEmpty else { completion(nil, error); return }
        completion(pairs.compactMap { payload, endpoint in
          guard case let .hostPort(host, port) = endpoint else { return nil }
          return ProxyDatagram(payload: payload, endpoint: datagramEndpoint(host: String(describing: host),
                                                                            port: port.rawValue))
        }, error)
      }
      return
    }
    #endif
    flow.readDatagrams { datagrams, endpoints, error in
      guard let datagrams = datagrams, let endpoints = endpoints, !datagrams.isEmpty else {
        completion(nil, error)
        return
      }
      completion(zip(datagrams, endpoints).compactMap { payload, endpoint in
        guard let endpoint = endpoint as? NWHostEndpoint, let port = UInt16(endpoint.port) else { return nil }
        return ProxyDatagram(payload: payload, endpoint: datagramEndpoint(host: endpoint.hostname, port: port))
      }, error)
    }
  }

  func writeDatagrams(_ datagrams: [ProxyDatagram], completion: @escaping (Error?) -> Void) {
    let addressed = datagrams.compactMap { datagram in
      parseDatagramEndpoint(datagram.endpoint).map { (datagram.payload, $0.host, $0.port) }
    }
    guard !addressed.isEmpty else { completion(nil); return }
    #if compiler(>=6.0)
    if #available(macOS 15.0, *) {
      flow.writeDatagrams(addressed.map { payload, host, port in
        (payload, Network.NWEndpoint.hostPort(host: .init(host), port: .init(integerLiteral: port)))
      }, completionHandler: completion)
      return
    }
    #endif
    flow.writeDatagrams(addressed.map(\.0),
                        sentBy: addressed.map { NWHostEndpoint(hostname: $0.1, port: String($0.2)) },
                        completionHandler: completion)
  }

  func close(error: Error?) {
    let error = flowError(error)
    flow.closeReadWithError(error)
    flow.closeWriteWithError(error)
  }

  private func flowError(_ error: Error?) -> Error? {
    guard let error = error as NSError? else { return nil }
    if error.domain == NEAppProxyErrorDomain { return error }
    return NSError(domain: NEAppProxyErrorDomain, code: NEAppProxyFlowError.internal.rawValue,
                   userInfo: [NSUnderlyingErrorKey: error])
  }
}
