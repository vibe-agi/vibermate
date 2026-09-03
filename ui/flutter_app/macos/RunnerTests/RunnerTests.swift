import Cocoa
import CryptoKit
import FlutterMacOS
import Security
import XCTest
@testable import ViberMate

final class RunnerTests: XCTestCase {
  private var temporaryDirectory: URL!

  override func setUpWithError() throws {
    temporaryDirectory = FileManager.default.temporaryDirectory
      .appendingPathComponent("vibermate-preferences-\(UUID().uuidString)", isDirectory: true)
  }

  override func tearDownWithError() throws {
    if temporaryDirectory != nil {
      try? FileManager.default.removeItem(at: temporaryDirectory)
    }
    temporaryDirectory = nil
  }

  func testPreferencesBridgeRoundTripsOnePrivateAtomicContract() throws {
    let bridge = try WorkbenchPreferencesBridge(directory: temporaryDirectory)
    XCTAssertNil(try bridge.read())

    let first = validPreferences(section: "settings", language: "zh-CN")
    try bridge.write(first)
    let firstInode = try inode(bridge.stateURL)
    XCTAssertEqual(try bridge.read(), first)

    let second = validPreferences(section: "network", language: "en-US")
    try bridge.write(second)
    XCTAssertEqual(try bridge.read(), second)
    XCTAssertNotEqual(try inode(bridge.stateURL), firstInode)

    let attributes = try FileManager.default.attributesOfItem(atPath: bridge.stateURL.path)
    XCTAssertEqual((attributes[.posixPermissions] as? NSNumber)?.intValue, 0o600)
  }

  func testPreferencesBridgeRejectsGenericOrOversizedPayloads() throws {
    let bridge = try WorkbenchPreferencesBridge(directory: temporaryDirectory)
    XCTAssertThrowsError(try bridge.write(#"{"arbitrary":true}"#))
    XCTAssertThrowsError(try bridge.write(String(repeating: "x", count: 4_097)))
    XCTAssertThrowsError(try bridge.write(42))
    var unsupportedTheme = validPreferencesPayload(section: "settings", language: "en-US")
    unsupportedTheme["theme"] = "sepia"
    XCTAssertThrowsError(try bridge.write(encode(unsupportedTheme)))
    XCTAssertNil(try bridge.read())
  }

  func testThemePreferenceAcceptsSystemAndMapsAllWindowAppearances() throws {
    let bridge = try WorkbenchPreferencesBridge(directory: temporaryDirectory)
    var payload = validPreferencesPayload(section: "settings", language: "en-US")
    payload["theme"] = "system"
    let system = encode(payload)

    try bridge.write(system)
    XCTAssertEqual(try bridge.read(), system)
    XCTAssertEqual(WorkbenchWindowTheme.fromPreferences(system), .system)
    XCTAssertNil(WorkbenchWindowTheme.system.appearance)
    XCTAssertEqual(WorkbenchWindowTheme.light.appearance?.name, .aqua)
    XCTAssertEqual(WorkbenchWindowTheme.dark.appearance?.name, .darkAqua)
    XCTAssertEqual(WorkbenchWindowTheme.fromPreferences(nil), .system)
    XCTAssertEqual(WorkbenchWindowTheme.fromPreferences("not json"), .system)

    let window = NSWindow()
    WorkbenchWindowTheme.dark.apply(to: window)
    XCTAssertEqual(window.appearance?.name, .darkAqua)
    WorkbenchWindowTheme.light.apply(to: window)
    XCTAssertEqual(window.appearance?.name, .aqua)
    WorkbenchWindowTheme.system.apply(to: window)
    XCTAssertNil(window.appearance)
  }

  func testPreferencesBridgeRequiresAnEnvironmentForAHistoricalRevision() throws {
    let bridge = try WorkbenchPreferencesBridge(directory: temporaryDirectory)
    var payload = validPreferencesPayload(section: "environments", language: "en-US")
    payload["selectedEnvironmentRevision"] = 7
    XCTAssertThrowsError(try bridge.write(encode(payload)))

    payload["selectedEnvironmentId"] = "work"
    let historical = encode(payload)
    try bridge.write(historical)
    XCTAssertEqual(try bridge.read(), historical)
  }

  func testPreferencesBridgeRefusesASymbolicLinkWithoutReplacingItsTarget() throws {
    try FileManager.default.createDirectory(
      at: temporaryDirectory,
      withIntermediateDirectories: true,
      attributes: [.posixPermissions: 0o700]
    )
    let target = temporaryDirectory.appendingPathComponent("target")
    let targetData = Data("do-not-replace".utf8)
    try targetData.write(to: target)
    let state = temporaryDirectory.appendingPathComponent(
      WorkbenchPreferencesBridge.fileName
    )
    try FileManager.default.createSymbolicLink(at: state, withDestinationURL: target)

    let bridge = try WorkbenchPreferencesBridge(directory: temporaryDirectory)
    XCTAssertThrowsError(try bridge.read())
    let repaired = validPreferences(section: "captures", language: "en-US")
    try bridge.write(repaired)
    XCTAssertEqual(try Data(contentsOf: target), targetData)
    let attributes = try FileManager.default.attributesOfItem(atPath: state.path)
    XCTAssertEqual(attributes[.type] as? FileAttributeType, .typeRegular)
    XCTAssertEqual(try String(contentsOf: state, encoding: .utf8), repaired)
  }

  func testTerminationFlushesTheLastValidatedPayloadAndFencesLateWrites() throws {
    let bridge = try WorkbenchPreferencesBridge(directory: temporaryDirectory)
    let expected = validPreferences(section: "settings", language: "zh-CN")
    try bridge.write(expected)

    let sentinel = validPreferences(section: "captures", language: "en-US")
    try Data(sentinel.utf8).write(to: bridge.stateURL, options: .atomic)
    try FileManager.default.setAttributes(
      [.posixPermissions: 0o600],
      ofItemAtPath: bridge.stateURL.path
    )
    try bridge.close()

    XCTAssertEqual(try String(contentsOf: bridge.stateURL, encoding: .utf8), expected)
    XCTAssertThrowsError(try bridge.write(sentinel))
  }

  func testDefaultLocationUsesTheSharedDesktopApplicationIdentity() throws {
    let home = temporaryDirectory.appendingPathComponent("home", isDirectory: true)
    let bridge = try WorkbenchPreferencesBridge(environment: ["HOME": home.path])
    XCTAssertEqual(
      bridge.stateURL.path,
      home
        .appendingPathComponent("Library/Application Support", isDirectory: true)
        .appendingPathComponent("io.vibermate.desktop", isDirectory: true)
        .appendingPathComponent("ui-state", isDirectory: true)
        .appendingPathComponent(WorkbenchPreferencesBridge.fileName)
        .path
    )
  }

  func testFixedUserHomeIsolatesPreferencesWithoutReplacingLoginHome() throws {
    let fixedHome = temporaryDirectory.appendingPathComponent("fixed", isDirectory: true)
    let bridge = try WorkbenchPreferencesBridge(environment: [
      "HOME": "/Users/login-owner",
      "CFFIXED_USER_HOME": fixedHome.path,
    ])
    XCTAssertEqual(
      bridge.stateURL.path,
      fixedHome
        .appendingPathComponent("Library/Application Support", isDirectory: true)
        .appendingPathComponent("io.vibermate.desktop", isDirectory: true)
        .appendingPathComponent("ui-state", isDirectory: true)
        .appendingPathComponent(WorkbenchPreferencesBridge.fileName)
        .path
    )
  }

  func testFixedUserHomeCanonicalizesAMacOSFilesystemAlias() throws {
    let aliasedHome = "/private/tmp"
    let canonicalHome = URL(fileURLWithPath: aliasedHome, isDirectory: true)
      .standardizedFileURL
    if canonicalHome.path == aliasedHome {
      throw XCTSkip("This macOS volume does not expose the /private/tmp alias")
    }

    let bridge = try WorkbenchPreferencesBridge(environment: [
      "HOME": "/Users/login-owner",
      "CFFIXED_USER_HOME": aliasedHome,
    ])
    XCTAssertEqual(
      bridge.stateURL.path,
      canonicalHome
        .appendingPathComponent("Library/Application Support", isDirectory: true)
        .appendingPathComponent("io.vibermate.desktop", isDirectory: true)
        .appendingPathComponent("ui-state", isDirectory: true)
        .appendingPathComponent(WorkbenchPreferencesBridge.fileName)
        .path
    )
  }

  func testFixedUserHomeFailsClosedInsteadOfFallingBackToLoginHome() throws {
    for invalid in ["relative", "/", "/private/tmp/../tmp"] {
      XCTAssertThrowsError(
        try WorkbenchPreferencesBridge(environment: [
          "HOME": temporaryDirectory.path,
          "CFFIXED_USER_HOME": invalid,
        ])
      )
    }
  }

  func testRootTrustInstallerReceivesOnlyDigestBoundPublicMaterial() throws {
    let certificate = Data([0x30, 0x03, 0x01, 0x02, 0x03])
    let fingerprint = SHA256.hash(data: certificate)
      .map { String(format: "%02x", $0) }
      .joined()
    var installed: Data?
    let installer = RootTrustInstaller(installOperation: { material in
      installed = material
      return errSecSuccess
    })

    try installer.install([
      "schema": "vibermate-root-trust-install/v1",
      "rootRevision": 7,
      "fingerprint": fingerprint,
      "certificateDerBase64": certificate.base64EncodedString(),
    ])

    XCTAssertEqual(installed, certificate)
  }

  func testRootTrustInstallerRejectsUnboundOrExtendedInput() throws {
    var installs = 0
    let installer = RootTrustInstaller(installOperation: { _ in
      installs += 1
      return errSecSuccess
    })
    let certificate = Data([0x30, 0x01, 0x00])
    let base = [
      "schema": "vibermate-root-trust-install/v1",
      "rootRevision": 1,
      "fingerprint": String(repeating: "a", count: 64),
      "certificateDerBase64": certificate.base64EncodedString(),
    ] as [String: Any]
    XCTAssertThrowsError(try installer.install(base))
    var extended = base
    extended["path"] = "/tmp/foreign.cer"
    XCTAssertThrowsError(try installer.install(extended))
    XCTAssertEqual(installs, 0)
  }

  func testRootTrustInstallerClassifiesAuthorizationOutcomes() throws {
    let certificate = Data([0x30, 0x01, 0x00])
    let fingerprint = SHA256.hash(data: certificate)
      .map { String(format: "%02x", $0) }
      .joined()
    let payload = [
      "schema": "vibermate-root-trust-install/v1",
      "rootRevision": 1,
      "fingerprint": fingerprint,
      "certificateDerBase64": certificate.base64EncodedString(),
    ] as [String: Any]
    for fixture in [
      (errSecUserCanceled, RootTrustInstaller.Failure.userCancelled),
      (errAuthorizationCanceled, RootTrustInstaller.Failure.userCancelled),
      (errSecAuthFailed, RootTrustInstaller.Failure.permissionDenied),
      (errAuthorizationDenied, RootTrustInstaller.Failure.permissionDenied),
      (errSecWrPerm, RootTrustInstaller.Failure.permissionDenied),
      (OSStatus(-25337), RootTrustInstaller.Failure.permissionDenied),
      (errSecIO, RootTrustInstaller.Failure.installFailed),
    ] {
      let installer = RootTrustInstaller(installOperation: { _ in fixture.0 })
      XCTAssertThrowsError(try installer.install(payload)) { error in
        guard let observed = error as? RootTrustInstaller.Failure else {
          return XCTFail("unexpected Root trust error: \(error)")
        }
        XCTAssertEqual(observed, fixture.1)
      }
    }
  }

  private func inode(_ url: URL) throws -> UInt64 {
    let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
    return try XCTUnwrap((attributes[.systemFileNumber] as? NSNumber)?.uint64Value)
  }

  private func validPreferences(section: String, language: String) -> String {
    encode(validPreferencesPayload(section: section, language: language))
  }

  private func validPreferencesPayload(section: String, language: String) -> [String: Any] {
    [
      "schema": "vibermate-workbench-preferences/v2",
      "language": language,
      "theme": "light",
      "section": section,
      "selectedCaptureKey": NSNull(),
      "selectedEnvironmentId": NSNull(),
      "selectedEnvironmentRevision": NSNull(),
      "selectedEndpointId": NSNull(),
    ]
  }

  private func encode(_ payload: [String: Any]) -> String {
    let data = try! JSONSerialization.data(withJSONObject: payload, options: [.sortedKeys])
    return String(data: data, encoding: .utf8)!
  }
}
