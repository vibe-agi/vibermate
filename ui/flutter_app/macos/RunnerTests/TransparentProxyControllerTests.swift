import Foundation
import XCTest
#if !TRANSPARENT_CONTROLLER_STANDALONE
@testable import ViberMate
#endif

private final class FakeTransparentProxySystem: TransparentProxySystem {
  var onStateChanged: ((TransparentProxyState, String?) -> Void)?
  var activationCallbacks: [(Result<Void, Error>) -> Void] = []
  var starts: [[String: NSObject]] = []
  var stops = 0
  var stopFailure: Error?
  var pendingStop: ((Result<Void, Error>) -> Void)?
  var deferStop = false
  func activate(completion: @escaping (Result<Void, Error>) -> Void) {
    activationCallbacks.append(completion)
  }
  func start(options: [String: NSObject], completion: @escaping (Result<Void, Error>) -> Void) {
    starts.append(options)
    completion(.success(()))
  }
  func stop(completion: @escaping (Result<Void, Error>) -> Void) {
    stops += 1
    if let stopFailure { completion(.failure(stopFailure)); return }
    if deferStop { pendingStop = completion; return }
    onStateChanged?(.disabled, nil)
    completion(.success(()))
  }
  func approveFirst() throws {
    let callback = try XCTUnwrap(activationCallbacks.first)
    activationCallbacks.removeFirst()
    callback(.success(()))
  }
  func refresh() {}
  func openSettings() -> Bool { true }
}

@objcMembers
final class TransparentProxyControllerTests: XCTestCase {
  private let secret = String(repeating: "a", count: 43)

  private func configuration(_ id: String = "runtime-a", port: Int = 12345) -> [String: Any] {
    ["instanceId": id, "flowPort": port, "flowSecret": secret]
  }

  func testConfigurationDoesNotEnableCaptureWithoutUserChoice() throws {
    let system = FakeTransparentProxySystem()
    let controller = TransparentProxyController(system: system)
    try controller.configure(configuration())
    XCTAssertFalse(controller.enabled)
    XCTAssertTrue(system.activationCallbacks.isEmpty)
    XCTAssertTrue(system.starts.isEmpty)
  }

  func testEnableWaitsForApprovalAndPassesSecretOnlyAsStartOptions() throws {
    let system = FakeTransparentProxySystem()
    var saved: [Bool] = []
    let controller = TransparentProxyController(system: system, saveEnabled: { saved.append($0) })
    try controller.configure(configuration())
    controller.setEnabled(true)
    XCTAssertEqual(system.activationCallbacks.count, 1)
    XCTAssertEqual(saved, [true])
    XCTAssertTrue(system.starts.isEmpty)
    system.onStateChanged?(.approvalRequired, nil)
    XCTAssertEqual(controller.state, .approvalRequired)
    try system.approveFirst()
    XCTAssertEqual(system.starts.count, 1)
    XCTAssertEqual(system.starts.first?["flowSecret"] as? String, secret)
    XCTAssertEqual(system.starts.first?["flowPort"] as? Int, 12345)
    XCTAssertEqual(controller.state, .starting)
    system.onStateChanged?(.active, nil)
    XCTAssertEqual(controller.state, .active)
    let status = try JSONSerialization.data(withJSONObject: controller.snapshot)
    XCTAssertFalse(String(decoding: status, as: UTF8.self).contains(secret))
  }

  func testDisableDuringApprovalCannotStartAfterTheApprovalCompletes() throws {
    let system = FakeTransparentProxySystem()
    let controller = TransparentProxyController(system: system)
    try controller.configure(configuration())
    controller.setEnabled(true)
    XCTAssertEqual(system.activationCallbacks.count, 1)
    controller.setEnabled(false)
    try system.approveFirst()
    XCTAssertTrue(system.starts.isEmpty)
    XCTAssertFalse(controller.enabled)
    XCTAssertEqual(controller.state, .disabled)
    XCTAssertEqual(system.stops, 1)
  }

  func testNewRuntimeSupersedesPendingApprovalAndOldCloseIsIgnored() throws {
    let system = FakeTransparentProxySystem()
    let controller = TransparentProxyController(system: system)
    try controller.configure(configuration())
    controller.setEnabled(true)
    try controller.configure(configuration("runtime-b", port: 23456))
    controller.clearConfiguration(instanceId: "runtime-a")
    let callbacks = system.activationCallbacks
    callbacks.forEach { $0(.success(())) }
    XCTAssertEqual(system.starts.count, 1)
    XCTAssertEqual(system.starts.first?["flowPort"] as? Int, 23456)
    XCTAssertEqual(system.stops, 0)
    controller.clearConfiguration(instanceId: "runtime-b")
    XCTAssertEqual(system.stops, 1)
    XCTAssertEqual(controller.state, .unavailable)
    XCTAssertTrue(controller.enabled)
  }

  func testSameConfigurationDoesNotRestartAnActiveTunnel() throws {
    let system = FakeTransparentProxySystem()
    let controller = TransparentProxyController(system: system)
    try controller.configure(configuration())
    controller.setEnabled(true)
    try system.approveFirst()
    system.onStateChanged?(.active, nil)
    try controller.configure(configuration())
    XCTAssertEqual(system.starts.count, 1)
    XCTAssertTrue(system.activationCallbacks.isEmpty)
  }

  func testInvalidConfigurationDoesNotReplaceTheCurrentGeneration() throws {
    let system = FakeTransparentProxySystem()
    let controller = TransparentProxyController(system: system)
    try controller.configure(configuration())
    for invalid in [
      ["instanceId": "bad", "flowPort": 0, "flowSecret": secret],
      ["instanceId": "bad", "flowPort": 65536, "flowSecret": secret],
      ["instanceId": "bad", "flowPort": true, "flowSecret": secret],
      ["instanceId": "bad", "flowPort": 123.5, "flowSecret": secret],
      ["instanceId": "bad", "flowPort": 12345, "flowSecret": "short"],
      ["instanceId": "", "flowPort": 12345, "flowSecret": secret],
    ] as [[String: Any]] {
      XCTAssertThrowsError(try controller.configure(invalid))
    }
    controller.setEnabled(true)
    try system.approveFirst()
    XCTAssertEqual(system.starts.first?["flowPort"] as? Int, 12345)
  }

  func testEnabledPreferenceWaitsForNewRuntimeBeforeStarting() throws {
    let system = FakeTransparentProxySystem()
    let controller = TransparentProxyController(system: system, enabled: true)
    XCTAssertTrue(system.starts.isEmpty)
    try controller.configure(configuration())
    XCTAssertEqual(system.activationCallbacks.count, 1)
    try system.approveFirst()
    XCTAssertEqual(system.starts.count, 1)
  }

  func testFailedStopKeepsConnectedEvidenceAndErrorUntilDisconnected() throws {
    let system = FakeTransparentProxySystem()
    let controller = TransparentProxyController(system: system)
    try controller.configure(configuration())
    controller.setEnabled(true)
    try system.approveFirst()
    system.onStateChanged?(.active, nil)
    system.stopFailure = NSError(domain: "test.stop", code: 7,
                                userInfo: [NSLocalizedDescriptionKey: "Cannot load network configuration"])

    controller.setEnabled(false)
    XCTAssertFalse(controller.enabled)
    XCTAssertEqual(controller.state, .unavailable)
    let stopError = try XCTUnwrap(controller.error)
    system.onStateChanged?(.active, nil)
    XCTAssertEqual(controller.state, .active)
    XCTAssertEqual(controller.error, stopError)

    system.onStateChanged?(.disabled, nil)
    XCTAssertEqual(controller.state, .disabled)
    XCTAssertNil(controller.error)
  }

  func testStopCompletionDoesNotClaimDisconnectedBeforeOSNotification() throws {
    let system = FakeTransparentProxySystem()
    let controller = TransparentProxyController(system: system)
    try controller.configure(configuration())
    controller.setEnabled(true)
    try system.approveFirst()
    system.onStateChanged?(.active, nil)
    system.deferStop = true

    controller.setEnabled(false)
    XCTAssertFalse(controller.enabled)
    XCTAssertEqual(controller.state, .active)
    try XCTUnwrap(system.pendingStop)(.success(()))
    XCTAssertEqual(controller.state, .active)

    system.onStateChanged?(.disabled, nil)
    XCTAssertEqual(controller.state, .disabled)
  }
}

#if TRANSPARENT_CONTROLLER_STANDALONE
@main
enum TransparentProxyControllerTestRunner {
  static func main() {
    let suite = XCTestSuite(forTestCaseClass: TransparentProxyControllerTests.self)
    suite.run()
    guard let result = suite.testRun else { exit(1) }
    print("Transparent controller: \(result.executionCount) tests, \(result.totalFailureCount) failures")
    exit(result.executionCount == 9 && result.totalFailureCount == 0 ? 0 : 1)
  }
}
#endif
