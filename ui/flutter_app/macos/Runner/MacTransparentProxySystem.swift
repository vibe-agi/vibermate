import AppKit
import NetworkExtension
import SystemExtensions

/// The OS-facing part of transparent capture. All methods and notifications
/// run on the main queue; preference writes and tunnel restarts are serialized.
final class MacTransparentProxySystem: NSObject, TransparentProxySystem, OSSystemExtensionRequestDelegate {
  static let extensionIdentifier = "io.vibermate.desktop.transparent-proxy"
  static let enabledPreference = "transparentCaptureEnabled"
  var onStateChanged: ((TransparentProxyState, String?) -> Void)?

  private var manager: NETransparentProxyManager?
  private var observation: NSObjectProtocol?
  private var activationRequest: OSSystemExtensionRequest?
  private var propertiesRequest: OSSystemExtensionRequest?
  private var activationCallbacks: [(Result<Void, Error>) -> Void] = []
  private var activated = false
  private var awaitingApproval = false
  private var desiredOperation: UInt64 = 0
  private var actions: [() -> Void] = []
  private var busy = false

  override init() {
    super.init()
    observation = NotificationCenter.default.addObserver(
      forName: .NEVPNStatusDidChange, object: nil, queue: .main
    ) { [weak self] notification in
      guard let self, let connection = notification.object as? NEVPNConnection,
            connection === self.manager?.connection else { return }
      self.reportConnectionState()
    }
  }

  deinit {
    if let observation { NotificationCenter.default.removeObserver(observation) }
  }

  private static func failure(_ description: String, code: Int = 1) -> NSError {
    NSError(domain: "io.vibermate.transparent-capture", code: code,
            userInfo: [NSLocalizedDescriptionKey: description])
  }

  func activate(completion: @escaping (Result<Void, Error>) -> Void) {
    guard Bundle.main.bundleIdentifier == "io.vibermate.desktop" else {
      completion(.failure(Self.failure("Transparent capture requires the installed ViberMate desktop app.")))
      return
    }
    let location = Bundle.main.bundleURL.appendingPathComponent(
      "Contents/Library/SystemExtensions/\(Self.extensionIdentifier).systemextension"
    )
    guard FileManager.default.fileExists(atPath: location.path) else {
      completion(.failure(Self.failure("The ViberMate network extension is missing from this app.")))
      return
    }
    if activated { completion(.success(())); return }
    activationCallbacks.append(completion)
    guard activationRequest == nil else {
      if awaitingApproval { onStateChanged?(.approvalRequired, nil) }
      return
    }
    let request = OSSystemExtensionRequest.activationRequest(
      forExtensionWithIdentifier: Self.extensionIdentifier, queue: .main
    )
    request.delegate = self
    activationRequest = request
    OSSystemExtensionManager.shared.submitRequest(request)
  }

  func start(options: [String: NSObject], completion: @escaping (Result<Void, Error>) -> Void) {
    enqueue { [weak self] operation, finish in
      guard let self else { finish(); return }
      self.loadManager { result in
        guard self.current(operation) else { finish(); return }
        switch result {
        case .failure(let error): completion(.failure(error)); finish()
        case .success(let existing):
          let manager = existing ?? NETransparentProxyManager()
          self.manager = manager
          manager.connection.stopVPNTunnel()
          self.waitForDisconnect(manager, operation: operation, deadline: Date().addingTimeInterval(10)) { result in
            guard self.current(operation) else { finish(); return }
            if case .failure(let error) = result { completion(.failure(error)); finish(); return }
            let protocolConfiguration = NETunnelProviderProtocol()
            protocolConfiguration.providerBundleIdentifier = Self.extensionIdentifier
            protocolConfiguration.serverAddress = "127.0.0.1"
            // Only stable non-secret settings are persisted. The changing
            // Runtime port/secret travel in startVPNTunnel(options:) below.
            protocolConfiguration.providerConfiguration = nil
            manager.protocolConfiguration = protocolConfiguration
            manager.localizedDescription = "ViberMate"
            manager.isOnDemandEnabled = false
            manager.isEnabled = true
            manager.saveToPreferences { error in
              DispatchQueue.main.async {
                guard self.current(operation) else { finish(); return }
                if let error { completion(.failure(error)); finish(); return }
                manager.loadFromPreferences { error in
                  DispatchQueue.main.async {
                    guard self.current(operation) else { finish(); return }
                    if let error { completion(.failure(error)); finish(); return }
                    do {
                      try manager.connection.startVPNTunnel(options: options)
                      self.onStateChanged?(.starting, nil)
                      completion(.success(()))
                    } catch {
                      completion(.failure(error))
                    }
                    finish()
                  }
                }
              }
            }
          }
        }
      }
    }
  }

  func stop(completion: @escaping (Result<Void, Error>) -> Void) {
    enqueue { [weak self] operation, finish in
      guard let self else { finish(); return }
      self.loadManager { result in
        guard self.current(operation) else { finish(); return }
        switch result {
        case .failure(let error): completion(.failure(error)); finish()
        case .success(nil):
          self.onStateChanged?(.disabled, nil)
          completion(.success(()))
          finish()
        case .success(let manager?):
          self.manager = manager
          manager.connection.stopVPNTunnel()
          manager.isEnabled = false
          manager.isOnDemandEnabled = false
          // Clear any providerConfiguration from an older implementation.
          (manager.protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration = nil
          manager.saveToPreferences { error in
            DispatchQueue.main.async {
              if self.current(operation) {
                completion(error.map { .failure($0) } ?? .success(()))
              }
              finish()
              if self.current(operation), error == nil { self.reportConnectionState() }
            }
          }
        }
      }
    }
  }

  private func enqueue(_ action: @escaping (UInt64, @escaping () -> Void) -> Void) {
    desiredOperation &+= 1
    let operation = desiredOperation
    actions.append { [weak self] in
      guard let self else { return }
      action(operation) {
        self.busy = false
        self.performNext()
      }
    }
    performNext()
  }

  private func performNext() {
    guard !busy, !actions.isEmpty else { return }
    busy = true
    actions.removeFirst()()
  }

  private func current(_ operation: UInt64) -> Bool { operation == desiredOperation }

  private func loadManager(_ completion: @escaping (Result<NETransparentProxyManager?, Error>) -> Void) {
    NETransparentProxyManager.loadAllFromPreferences { managers, error in
      DispatchQueue.main.async {
        if let error { completion(.failure(error)); return }
        let ours = managers?.first {
          ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.extensionIdentifier
        }
        completion(.success(ours))
      }
    }
  }

  private func waitForDisconnect(
    _ manager: NETransparentProxyManager, operation: UInt64, deadline: Date,
    completion: @escaping (Result<Void, Error>) -> Void
  ) {
    guard current(operation) else { completion(.success(())); return }
    switch manager.connection.status {
    case .invalid, .disconnected: completion(.success(()))
    default:
      guard Date() < deadline else {
        completion(.failure(Self.failure("The previous transparent capture connection did not stop in time.")))
        return
      }
      DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) {
        self.waitForDisconnect(manager, operation: operation, deadline: deadline, completion: completion)
      }
    }
  }

  func refresh() {
    guard activationRequest == nil, propertiesRequest == nil, !busy else { return }
    let request = OSSystemExtensionRequest.propertiesRequest(
      forExtensionWithIdentifier: Self.extensionIdentifier, queue: .main
    )
    request.delegate = self
    propertiesRequest = request
    OSSystemExtensionManager.shared.submitRequest(request)
  }

  func openSettings() -> Bool {
    guard let url = URL(string: "x-apple.systempreferences:com.apple.LoginItems-Settings.extension") else { return false }
    return NSWorkspace.shared.open(url)
  }

  private func reportConnectionState() {
    guard let manager else { onStateChanged?(.disabled, nil); return }
    switch manager.connection.status {
    case .connected: onStateChanged?(.active, nil)
    case .connecting, .reasserting: onStateChanged?(.starting, nil)
    case .disconnecting:
      if busy { onStateChanged?(.starting, nil) }
    case .disconnected, .invalid:
      if busy { return }
      onStateChanged?(manager.isEnabled ? .unavailable : .disabled,
                      manager.isEnabled ? "network_extension_disconnected" : nil)
    @unknown default: onStateChanged?(.unavailable, "network_extension_status_unknown")
    }
  }

  func requestNeedsUserApproval(_ request: OSSystemExtensionRequest) {
    guard request === activationRequest else { return }
    awaitingApproval = true
    onStateChanged?(.approvalRequired, nil)
  }

  func request(
    _ request: OSSystemExtensionRequest,
    actionForReplacingExtension existing: OSSystemExtensionProperties,
    withExtension ext: OSSystemExtensionProperties
  ) -> OSSystemExtensionRequest.ReplacementAction { .replace }

  func request(_ request: OSSystemExtensionRequest, didFinishWithResult result: OSSystemExtensionRequest.Result) {
    guard request === activationRequest else {
      if request === propertiesRequest { propertiesRequest = nil }
      return
    }
    activationRequest = nil
    awaitingApproval = false
    activated = result == .completed
    let callbacks = activationCallbacks
    activationCallbacks.removeAll()
    let outcome: Result<Void, Error> = activated
      ? .success(())
      : .failure(Self.failure("Restart this Mac to finish activating the ViberMate network extension."))
    callbacks.forEach { $0(outcome) }
  }

  func request(_ request: OSSystemExtensionRequest, didFailWithError error: Error) {
    if request === propertiesRequest {
      propertiesRequest = nil
      onStateChanged?(.unavailable, error.localizedDescription)
    }
    guard request === activationRequest else { return }
    activationRequest = nil
    awaitingApproval = false
    activated = false
    let callbacks = activationCallbacks
    activationCallbacks.removeAll()
    callbacks.forEach { $0(.failure(error)) }
  }

  func request(_ request: OSSystemExtensionRequest, foundProperties properties: [OSSystemExtensionProperties]) {
    guard request === propertiesRequest else { return }
    propertiesRequest = nil
    if properties.contains(where: { $0.isAwaitingUserApproval }) {
      onStateChanged?(.approvalRequired, nil)
    } else if properties.contains(where: { $0.isEnabled && !$0.isUninstalling }) {
      loadManager { [weak self] result in
        guard let self, !self.busy, self.activationRequest == nil else { return }
        switch result {
        case .failure(let error): self.onStateChanged?(.unavailable, error.localizedDescription)
        case .success(let manager):
          self.manager = manager
          self.reportConnectionState()
        }
      }
    } else {
      activated = false
      onStateChanged?(.notInstalled, nil)
    }
  }
}
