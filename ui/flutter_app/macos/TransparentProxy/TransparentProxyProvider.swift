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
        do { try current.broker.hello() }
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
    guard let tcp = flow as? NEAppProxyTCPFlow,
          let identity = FlowSourceIdentity(auditToken: flow.metaData.sourceAppAuditToken) else { return false }
    sessionLock.lock()
    let current = session
    sessionLock.unlock()
    guard let current = current else { return false }
    guard let destination = Self.destination(tcp) else {
      return current.wasOwned(identity) ? Self.refuse(tcp) : false
    }
    if destination.isLoopback { return false }

    do {
      guard let socket = try current.broker.query(pid: identity.pid) else {
        current.recordUnowned(identity)
        return false
      }
      current.recordOwned(identity)
      do {
        let header = try destination.header()
        if !current.add(tcp, socket: socket, header: header) { return Self.refuse(tcp) }
      } catch {
        socket.close()
        return Self.refuse(tcp)
      }
      return true
    } catch {
      // The v1 broker cannot distinguish a first-seen owned process from an
      // unrelated process on query failure. Known audit identities are refused;
      // unknown identities retain the protocol's fail-open behavior.
      return current.wasOwned(identity) ? Self.refuse(tcp) : false
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

  private static func refuse(_ flow: NEAppProxyTCPFlow) -> Bool {
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
    // nil endpoints match all outbound TCP. Wildcard IP + port 0 is not a
    // valid transparent-proxy rule. The /8 also excludes the broker socket.
    settings.includedNetworkRules = [rule(host: nil, prefix: 0)]
    settings.excludedNetworkRules = [rule(host: "127.0.0.0", prefix: 8), rule(host: "::1", prefix: 128)]
    return settings
  }

  private static func rule(host: String?, prefix: Int) -> NENetworkRule {
    #if compiler(>=6.0)
    if #available(macOS 15.0, *) {
      let endpoint = host.map { Network.NWEndpoint.hostPort(host: .init($0), port: 0) }
      return NENetworkRule(remoteNetworkEndpoint: endpoint, remotePrefix: prefix,
                           localNetworkEndpoint: nil, localPrefix: 0, protocol: .TCP, direction: .outbound)
    }
    #endif
    return NENetworkRule(remoteNetwork: host.map { NWHostEndpoint(hostname: $0, port: "0") },
                         remotePrefix: prefix, localNetwork: nil, localPrefix: 0,
                         protocol: .TCP, direction: .outbound)
  }
}

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
  private var relays: [UUID: TCPFlowRelay] = [:]
  private var history = FlowOwnershipHistory()
  private var stopped = false

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
      try? self?.broker.hello()
    }
    self.timer = timer
    timer.resume()
  }

  func stop(completion: @escaping () -> Void = {}) {
    lock.lock()
    stopped = true
    let timer = self.timer
    self.timer = nil
    let active = Array(relays.values)
    lock.unlock()
    timer?.cancel()
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
    lock.lock()
    guard !stopped else { lock.unlock(); socket.close(); return false }
    let id = UUID()
    let group = relayGroup
    group.enter()
    let relay = TCPFlowRelay(flow: SystemTCPFlow(flow), socket: socket) { [weak self] in
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
