import Cocoa
import FlutterMacOS
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
    XCTAssertNil(try bridge.read())
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

  private func inode(_ url: URL) throws -> UInt64 {
    let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
    return try XCTUnwrap((attributes[.systemFileNumber] as? NSNumber)?.uint64Value)
  }

  private func validPreferences(section: String, language: String) -> String {
    encode(validPreferencesPayload(section: section, language: language))
  }

  private func validPreferencesPayload(section: String, language: String) -> [String: Any] {
    [
      "schema": "vibermate-workbench-preferences/v1",
      "language": language,
      "section": section,
      "selectedCaptureKey": NSNull(),
      "selectedConversationKey": NSNull(),
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
