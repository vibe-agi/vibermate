import Cocoa
import CryptoKit
import Darwin
import FlutterMacOS
import Security

enum WorkbenchWindowTheme: String {
  case system
  case light
  case dark

  static func fromPreferences(_ encoded: String?) -> WorkbenchWindowTheme {
    guard let encoded,
          let data = encoded.data(using: .utf8),
          let object = try? JSONSerialization.jsonObject(with: data),
          let payload = object as? [String: Any],
          let value = payload["theme"] as? String,
          let theme = WorkbenchWindowTheme(rawValue: value) else {
      return .system
    }
    return theme
  }

  var appearance: NSAppearance? {
    switch self {
    case .system:
      return nil
    case .light:
      return NSAppearance(named: .aqua)
    case .dark:
      return NSAppearance(named: .darkAqua)
    }
  }

  func apply(to window: NSWindow) {
    window.appearance = appearance
    window.backgroundColor = .windowBackgroundColor
  }
}

class MainFlutterWindow: NSWindow {
  private static let preferencesChannelName = "io.vibermate.desktop/preferences"
  private static let rootTrustInstallerChannelName =
    "io.vibermate.desktop/root-trust-installer"
  private static let frameAutosaveName = "ViberMateMainWindow"

  private var preferencesChannel: FlutterMethodChannel?
  private var preferencesBridge: WorkbenchPreferencesBridge?
  private var rootTrustInstallerChannel: FlutterMethodChannel?
  private var rootTrustInstaller: RootTrustInstaller?

  override func awakeFromNib() {
    let flutterViewController = FlutterViewController()
    let windowFrame = self.frame
    self.contentViewController = flutterViewController
    self.setFrame(windowFrame, display: true)

    self.title = "ViberMate"
    self.titleVisibility = .hidden
    self.minSize = NSSize(width: 390, height: 620)
    if !self.setFrameUsingName(Self.frameAutosaveName) {
      self.setContentSize(NSSize(width: 1180, height: 760))
      self.center()
    }
    self.setFrameAutosaveName(Self.frameAutosaveName)

    RegisterGeneratedPlugins(registry: flutterViewController)
    preferencesBridge = try? WorkbenchPreferencesBridge()
    applyWindowTheme(
      WorkbenchWindowTheme.fromPreferences(try? preferencesBridge?.read())
    )
    let channel = FlutterMethodChannel(
      name: Self.preferencesChannelName,
      binaryMessenger: flutterViewController.engine.binaryMessenger
    )
    channel.setMethodCallHandler { [weak self] call, result in
      self?.handlePreferences(call: call, result: result)
    }
    self.preferencesChannel = channel

    rootTrustInstaller = RootTrustInstaller()
    let rootInstallerChannel = FlutterMethodChannel(
      name: Self.rootTrustInstallerChannelName,
      binaryMessenger: flutterViewController.engine.binaryMessenger
    )
    rootInstallerChannel.setMethodCallHandler { [weak self] call, result in
      self?.handleRootTrustInstaller(call: call, result: result)
    }
    self.rootTrustInstallerChannel = rootInstallerChannel

    super.awakeFromNib()
    self.sharingType = .readOnly
  }

  private func handlePreferences(call: FlutterMethodCall, result: @escaping FlutterResult) {
    if call.method == "setWorkbenchTheme" {
      guard let value = call.arguments as? String,
            let theme = WorkbenchWindowTheme(rawValue: value) else {
        result(preferencesError("invalid_arguments", "Theme must be system, light, or dark"))
        return
      }
      applyWindowTheme(theme)
      result(nil)
      return
    }
    guard let preferencesBridge else {
      result(preferencesError("unavailable", "Preferences storage is unavailable"))
      return
    }
    switch call.method {
    case "readWorkbenchPreferences":
      guard call.arguments == nil else {
        result(preferencesError("invalid_arguments", "Preferences read accepts no arguments"))
        return
      }
      do {
        let encoded = try preferencesBridge.read()
        applyWindowTheme(WorkbenchWindowTheme.fromPreferences(encoded))
        result(encoded)
      } catch {
        result(preferencesError("invalid_state", "Stored preferences are invalid"))
      }
    case "writeWorkbenchPreferences":
      do {
        try preferencesBridge.write(call.arguments)
        applyWindowTheme(
          WorkbenchWindowTheme.fromPreferences(call.arguments as? String)
        )
        result(nil)
      } catch WorkbenchPreferencesBridge.Failure.invalidPayload {
        result(preferencesError("invalid_arguments", "Preferences payload is invalid"))
      } catch {
        result(preferencesError("write_failed", "Preferences could not be persisted"))
      }
    default:
      result(FlutterMethodNotImplemented)
    }
  }

  private func preferencesError(_ code: String, _ message: String) -> FlutterError {
    FlutterError(code: code, message: message, details: nil)
  }

  private func handleRootTrustInstaller(
    call: FlutterMethodCall,
    result: @escaping FlutterResult
  ) {
    guard call.method == "installRootCertificate" else {
      result(FlutterMethodNotImplemented)
      return
    }
    guard let rootTrustInstaller else {
      result(preferencesError("unavailable", "Root trust installer is unavailable"))
      return
    }
    do {
      try rootTrustInstaller.install(call.arguments)
      result(nil)
    } catch RootTrustInstaller.Failure.invalidPayload {
      result(preferencesError("invalid_arguments", "Root certificate material is invalid"))
    } catch RootTrustInstaller.Failure.userCancelled {
      result(preferencesError("user_cancelled", "macOS authorization was cancelled"))
    } catch RootTrustInstaller.Failure.permissionDenied {
      result(preferencesError("permission_denied", "macOS authorization was denied"))
    } catch {
      result(preferencesError("install_failed", "Root certificate trust could not be changed"))
    }
  }

  private func applyWindowTheme(_ theme: WorkbenchWindowTheme) {
    theme.apply(to: self)
  }

  func prepareForApplicationTermination() throws {
    try preferencesBridge?.close()
  }
}

final class WorkbenchPreferencesBridge {
  enum Failure: Error {
    case invalidState
    case invalidPayload
    case writeFailed
    case closed
  }

  static let maximumBytes = 4_096
  static let fileName = "workbench-preferences-v2.json"
  private static let applicationID = "io.vibermate.desktop"
  private static let schema = "vibermate-workbench-preferences/v2"
  private static let fields: Set<String> = [
    "schema",
    "language",
    "theme",
    "section",
    "selectedCaptureKey",
    "selectedEnvironmentId",
    "selectedEnvironmentRevision",
    "selectedEndpointId",
  ]
  private static let resourcePattern = #"^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$"#

  let stateURL: URL
  private let directoryURL: URL
  private let lock = NSLock()
  private var closing = false
  private var lastValidPayload: String?

  convenience init(
    environment: [String: String] = ProcessInfo.processInfo.environment
  ) throws {
    let selectedHome = environment["CFFIXED_USER_HOME"] ?? environment["HOME"]
    guard let home = selectedHome,
          home.hasPrefix("/"),
          home != "/",
          URL(fileURLWithPath: home, isDirectory: true).standardized.path == home else {
      throw Failure.invalidState
    }
    // Reject lexical traversal above, then resolve macOS filesystem aliases
    // such as /private/tmp -> /tmp once. Requiring the unresolved spelling to
    // equal standardizedFileURL would reject a valid CFFIXED_USER_HOME and
    // disable the entire preferences channel during an isolated launch.
    let canonicalHome = URL(fileURLWithPath: home, isDirectory: true)
      .standardizedFileURL
    guard canonicalHome.path.hasPrefix("/"), canonicalHome.path != "/" else {
      throw Failure.invalidState
    }
    let directory = canonicalHome
      .appendingPathComponent("Library", isDirectory: true)
      .appendingPathComponent("Application Support", isDirectory: true)
      .appendingPathComponent(Self.applicationID, isDirectory: true)
      .appendingPathComponent("ui-state", isDirectory: true)
    try self.init(directory: directory)
  }

  init(directory: URL) throws {
    let normalized = directory.standardizedFileURL
    guard directory.isFileURL,
          normalized.path.hasPrefix("/"),
          normalized.path == directory.path else {
      throw Failure.invalidState
    }
    directoryURL = normalized
    stateURL = normalized.appendingPathComponent(Self.fileName, isDirectory: false)
  }

  func read() throws -> String? {
    lock.lock()
    defer { lock.unlock() }
    guard !closing else { throw Failure.closed }
    let encoded = try readPayload()
    if let encoded, Self.valid(encoded) {
      lastValidPayload = encoded
    }
    return encoded
  }

  func write(_ value: Any?) throws {
    guard let encoded = value as? String, Self.valid(encoded) else {
      throw Failure.invalidPayload
    }
    lock.lock()
    defer { lock.unlock() }
    guard !closing else { throw Failure.closed }
    try commit(encoded)
    lastValidPayload = encoded
  }

  func close() throws {
    lock.lock()
    defer { lock.unlock() }
    if closing { return }
    closing = true
    guard let lastValidPayload else { return }
    do {
      try commit(lastValidPayload)
    } catch {
      closing = false
      throw error
    }
  }

  private func readPayload() throws -> String? {
    var metadata = stat()
    if lstat(stateURL.path, &metadata) != 0 {
      if errno == ENOENT { return nil }
      throw Failure.invalidState
    }
    guard Self.privateRegularFile(metadata) else { throw Failure.invalidState }
    let descriptor = Darwin.open(stateURL.path, O_RDONLY | O_NOFOLLOW)
    guard descriptor >= 0 else { throw Failure.invalidState }
    let handle = FileHandle(fileDescriptor: descriptor, closeOnDealloc: true)
    var opened = stat()
    guard fstat(descriptor, &opened) == 0,
          Self.privateRegularFile(opened),
          opened.st_dev == metadata.st_dev,
          opened.st_ino == metadata.st_ino else {
      try? handle.close()
      throw Failure.invalidState
    }
    // fstat already bounded the exact opened inode, so the deployment-target
    // compatible API cannot turn this into an unbounded read.
    let data = handle.readDataToEndOfFile()
    handle.closeFile()
    guard !data.isEmpty,
          data.count <= Self.maximumBytes,
          let encoded = String(data: data, encoding: .utf8) else {
      throw Failure.invalidState
    }
    return encoded
  }

  private func commit(_ encoded: String) throws {
    guard let data = encoded.data(using: .utf8),
          !data.isEmpty,
          data.count <= Self.maximumBytes else {
      throw Failure.writeFailed
    }
    try prepareDirectory()
    let temporaryURL = directoryURL.appendingPathComponent(
      ".workbench-preferences-\(UUID().uuidString).tmp",
      isDirectory: false
    )
    var descriptor = Darwin.open(
      temporaryURL.path,
      O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW,
      S_IRUSR | S_IWUSR
    )
    guard descriptor >= 0 else { throw Failure.writeFailed }
    var temporaryExists = true
    defer {
      if descriptor >= 0 { Darwin.close(descriptor) }
      if temporaryExists { Darwin.unlink(temporaryURL.path) }
    }
    let wroteAll = data.withUnsafeBytes { bytes -> Bool in
      guard let base = bytes.baseAddress else { return false }
      var offset = 0
      while offset < bytes.count {
        let count = Darwin.write(
          descriptor,
          base.advanced(by: offset),
          bytes.count - offset
        )
        if count <= 0 { return false }
        offset += count
      }
      return true
    }
    guard wroteAll,
          fchmod(descriptor, S_IRUSR | S_IWUSR) == 0,
          fsync(descriptor) == 0 else { throw Failure.writeFailed }
    guard Darwin.close(descriptor) == 0 else {
      descriptor = -1
      throw Failure.writeFailed
    }
    descriptor = -1
    guard rename(temporaryURL.path, stateURL.path) == 0 else {
      throw Failure.writeFailed
    }
    temporaryExists = false
    let directoryDescriptor = Darwin.open(directoryURL.path, O_RDONLY)
    guard directoryDescriptor >= 0 else { throw Failure.writeFailed }
    defer { Darwin.close(directoryDescriptor) }
    guard fsync(directoryDescriptor) == 0 else { throw Failure.writeFailed }
  }

  private func prepareDirectory() throws {
    do {
      try FileManager.default.createDirectory(
        at: directoryURL,
        withIntermediateDirectories: true,
        attributes: [.posixPermissions: 0o700]
      )
    } catch {
      throw Failure.writeFailed
    }
    var metadata = stat()
    guard lstat(directoryURL.path, &metadata) == 0,
          (metadata.st_mode & S_IFMT) == S_IFDIR,
          metadata.st_uid == geteuid(),
          chmod(directoryURL.path, S_IRWXU) == 0 else {
      throw Failure.writeFailed
    }
  }

  private static func privateRegularFile(_ metadata: stat) -> Bool {
    (metadata.st_mode & S_IFMT) == S_IFREG &&
      metadata.st_uid == geteuid() &&
      (metadata.st_mode & 0o777) == (S_IRUSR | S_IWUSR) &&
      metadata.st_size >= 1 &&
      metadata.st_size <= maximumBytes
  }

  private static func valid(_ encoded: String) -> Bool {
    guard let data = encoded.data(using: .utf8),
          !data.isEmpty,
          data.count <= maximumBytes,
          let object = try? JSONSerialization.jsonObject(with: data),
          let payload = object as? [String: Any] else {
      return false
    }
    let observedFields = Set(payload.keys)
    let activeSections: Set<String> = [
      "captures", "environments", "routes", "network", "settings",
    ]
    guard fields.isSubset(of: observedFields),
          observedFields.isSubset(of: fields),
          payload["schema"] as? String == schema,
          let language = payload["language"] as? String,
          ["en-US", "zh-CN"].contains(language),
          let theme = payload["theme"] as? String,
          ["system", "light", "dark"].contains(theme),
          let section = payload["section"] as? String,
          activeSections.contains(section),
          validSelection(
            payload["selectedCaptureKey"],
            prefixes: ["managed_run:", "manual_capture:"]
          ),
          validResource(payload["selectedEnvironmentId"]),
          validPositiveInteger(payload["selectedEnvironmentRevision"]),
          validResource(payload["selectedEndpointId"]) else {
      return false
    }
    if !(payload["selectedEnvironmentRevision"] is NSNull),
       payload["selectedEnvironmentId"] is NSNull {
      return false
    }
    return true
  }

  private static func validSelection(_ value: Any?, prefixes: [String]) -> Bool {
    if value is NSNull { return true }
    guard let selection = value as? String,
          selection.utf8.count <= 256 else { return false }
    for prefix in prefixes where selection.hasPrefix(prefix) {
      return matchesResource(String(selection.dropFirst(prefix.count)))
    }
    return false
  }

  private static func validResource(_ value: Any?) -> Bool {
    if value is NSNull { return true }
    guard let resource = value as? String else { return false }
    return matchesResource(resource)
  }

  private static func validPositiveInteger(_ value: Any?) -> Bool {
    if value is NSNull { return true }
    guard let number = value as? NSNumber,
          CFGetTypeID(number) != CFBooleanGetTypeID(),
          number.doubleValue.isFinite,
          number.doubleValue >= 1,
          number.doubleValue.rounded(.towardZero) == number.doubleValue else {
      return false
    }
    return true
  }

  private static func matchesResource(_ value: String) -> Bool {
    value.range(of: resourcePattern, options: .regularExpression) != nil
  }
}

final class RootTrustInstaller {
  enum Failure: Error, Equatable {
    case invalidPayload
    case userCancelled
    case permissionDenied
    case installFailed
  }

  private static let schema = "vibermate-root-trust-install/v1"
  private static let fields: Set<String> = [
    "schema", "rootRevision", "fingerprint", "certificateDerBase64",
  ]
  private static let fingerprintPattern = "^[0-9a-f]{64}$"
  private static let maximumCertificateBytes = 64 * 1_024
  private let installOperation: (Data) -> OSStatus

  convenience init() {
    self.init(installOperation: Self.installIntoCurrentUserTrust)
  }

  init(installOperation: @escaping (Data) -> OSStatus) {
    self.installOperation = installOperation
  }

  func install(_ arguments: Any?) throws {
    let certificate = try Self.certificate(arguments)
    switch installOperation(certificate) {
    case errSecSuccess:
      return
    case errSecUserCanceled, errAuthorizationCanceled:
      throw Failure.userCancelled
    case errSecAuthFailed, errSecInteractionNotAllowed, errSecReadOnly,
         errSecWrPerm, errAuthorizationDenied, -25337:
      throw Failure.permissionDenied
    default:
      throw Failure.installFailed
    }
  }

  private static func certificate(
    _ arguments: Any?
  ) throws -> Data {
    guard let payload = arguments as? [String: Any],
          Set(payload.keys) == fields,
          payload["schema"] as? String == schema,
          let revisionValue = payload["rootRevision"] as? NSNumber,
          CFGetTypeID(revisionValue) != CFBooleanGetTypeID(),
          revisionValue.doubleValue.isFinite,
          revisionValue.int64Value >= 1,
          revisionValue.doubleValue == Double(revisionValue.int64Value),
          let fingerprint = payload["fingerprint"] as? String,
          fingerprint.range(
            of: fingerprintPattern,
            options: .regularExpression
          ) != nil,
          let encoded = payload["certificateDerBase64"] as? String,
          encoded.utf8.count <= maximumCertificateBytes * 2,
          let der = Data(base64Encoded: encoded),
          !der.isEmpty,
          der.count <= maximumCertificateBytes,
          der.base64EncodedString() == encoded else {
      throw Failure.invalidPayload
    }
    let digest = SHA256.hash(data: der)
      .map { String(format: "%02x", $0) }
      .joined()
    guard digest == fingerprint else { throw Failure.invalidPayload }
    return der
  }

  private static func installIntoCurrentUserTrust(_ der: Data) -> OSStatus {
    guard let certificate = SecCertificateCreateWithData(nil, der as CFData) else {
      return errSecDecode
    }
    // SecItemAdd without an explicit keychain writes to this login's default
    // keychain. It deliberately avoids the privileged System.keychain path,
    // which a regular foreground application cannot modify directly.
    let addStatus = SecItemAdd([
      kSecClass: kSecClassCertificate,
      kSecValueRef: certificate,
    ] as CFDictionary, nil)
    guard addStatus == errSecSuccess || addStatus == errSecDuplicateItem else {
      return addStatus
    }
    let serverTLSPolicy = SecPolicyCreateSSL(true, nil)
    let trustSettings = [[
      kSecTrustSettingsPolicy: serverTLSPolicy,
      kSecTrustSettingsResult: NSNumber(
        value: SecTrustSettingsResult.trustRoot.rawValue
      ),
    ]] as CFArray
    // The trust applies only to the current macOS login and only to Server TLS.
    // No helper, shell, administrator credential, or system-wide mutation is
    // needed; the read-side observation verifies this same user trust domain.
    return SecTrustSettingsSetTrustSettings(
      certificate,
      .user,
      trustSettings
    )
  }
}
