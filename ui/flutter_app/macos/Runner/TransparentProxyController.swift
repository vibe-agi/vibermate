import Foundation
import CoreFoundation

enum TransparentProxyState: String {
  case notInstalled = "not_installed"
  case approvalRequired = "approval_required"
  case starting, active, disabled, unavailable
}

protocol TransparentProxySystem: AnyObject {
  var onStateChanged: ((TransparentProxyState, String?) -> Void)? { get set }
  func activate(completion: @escaping (Result<Void, Error>) -> Void)
  func start(options: [String: NSObject], completion: @escaping (Result<Void, Error>) -> Void)
  func stop(completion: @escaping (Result<Void, Error>) -> Void)
  func refresh()
  func openSettings() -> Bool
}

final class TransparentProxyController {
  private(set) var enabled: Bool
  private(set) var state: TransparentProxyState = .notInstalled
  private(set) var error: String?
  var onStatusChanged: (([String: Any]) -> Void)?
  private let system: TransparentProxySystem
  private let saveEnabled: (Bool) -> Void
  private var configuration: Configuration?
  private var revision: UInt64 = 0

  enum Failure: Error { case invalidConfiguration }

  // Runtime credentials live only here and in the per-start options. Neither
  // the persisted enabled preference nor any status sent to Flutter contains them.
  private struct Configuration: Equatable {
    let instanceID: String
    let port: Int
    let secret: String

    init(_ value: Any?) throws {
      guard let arguments = value as? [String: Any],
            let instanceID = arguments["instanceId"] as? String,
            !instanceID.isEmpty, instanceID.utf8.count <= 256,
            let number = arguments["flowPort"] as? NSNumber,
            CFGetTypeID(number) != CFBooleanGetTypeID(),
            number.doubleValue.isFinite,
            number.doubleValue.rounded() == number.doubleValue,
            (1...65535).contains(number.doubleValue),
            let secret = arguments["flowSecret"] as? String,
            secret.range(of: "^[A-Za-z0-9_-]{43}$", options: .regularExpression) != nil
      else { throw Failure.invalidConfiguration }
      self.instanceID = instanceID
      self.port = number.intValue
      self.secret = secret
    }

    var options: [String: NSObject] {
      ["flowPort": NSNumber(value: port), "flowSecret": secret as NSString]
    }
  }

  var snapshot: [String: Any] {
    var value: [String: Any] = ["state": state.rawValue, "enabled": enabled]
    if let error { value["error"] = error }
    return value
  }

  init(system: TransparentProxySystem, enabled: Bool = false, saveEnabled: @escaping (Bool) -> Void = { _ in }) {
    self.system = system
    self.enabled = enabled
    self.saveEnabled = saveEnabled
    system.onStateChanged = { [weak self] state, reason in
      guard let self else { return }
      if !self.enabled {
        // The user's preference is not evidence that the OS has stopped.
        // Keep a failed-stop reason until the OS confirms it is no longer active.
        let stopped = state == .disabled || state == .notInstalled
        self.update(state, error: stopped ? nil : reason ?? self.error)
      } else if self.configuration == nil {
        self.update(.unavailable, error: "runtime_unavailable")
      } else {
        self.update(state, error: reason)
      }
    }
  }

  func configure(_ arguments: Any?) throws {
    let next = try Configuration(arguments)
    guard configuration != next else { return }
    configuration = next
    revision &+= 1
    if enabled { startCurrentConfiguration() }
  }

  func setEnabled(_ value: Bool) {
    enabled = value
    saveEnabled(value)
    revision &+= 1
    if value {
      startCurrentConfiguration()
    } else {
      stopCurrentConfiguration(state: .disabled)
    }
  }

  func clearConfiguration(instanceId: String) {
    guard configuration?.instanceID == instanceId else { return }
    configuration = nil
    revision &+= 1
    stopCurrentConfiguration(state: enabled ? .unavailable : .disabled)
  }

  func refresh() { system.refresh() }
  func openSettings() -> Bool { system.openSettings() }

  private func startCurrentConfiguration() {
    guard enabled, configuration != nil else {
      update(.unavailable, error: "runtime_unavailable")
      return
    }
    let expected = revision
    update(.starting)
    system.activate { [weak self] result in
      guard let self, self.revision == expected, self.enabled,
            let configuration = self.configuration else { return }
      switch result {
      case .failure(let error):
        self.update(.unavailable, error: Self.describe(error))
      case .success:
        self.update(.starting)
        self.system.start(options: configuration.options) { [weak self] result in
          guard let self, self.revision == expected, self.enabled else { return }
          if case .failure(let error) = result {
            self.update(.unavailable, error: Self.describe(error))
          }
          // A successful start call only submits the request. The OS status
          // notification is what can move the UI from starting to active.
        }
      }
    }
  }

  private func stopCurrentConfiguration(state: TransparentProxyState) {
    let expected = revision
    if state == .unavailable {
      update(.unavailable, error: "runtime_unavailable")
    } else {
      update(self.state, error: error)
    }
    system.stop { [weak self] result in
      guard let self, self.revision == expected else { return }
      if case .failure(let error) = result {
        self.update(.unavailable, error: Self.describe(error))
      } else {
        // Saving a disabled preference submits the stop; the connection
        // notification or refreshed OS status confirms disconnection.
        self.system.refresh()
      }
    }
  }

  private func update(_ state: TransparentProxyState, error: String? = nil) {
    self.state = state
    self.error = error
    onStatusChanged?(snapshot)
  }

  private static func describe(_ error: Error) -> String {
    let value = error as NSError
    return "\(value.localizedDescription) (\(value.domain):\(value.code))"
  }
}
