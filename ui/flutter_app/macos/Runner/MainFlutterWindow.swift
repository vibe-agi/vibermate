import Cocoa
import Darwin
import FlutterMacOS

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
  private static let frameAutosaveName = "ViberMateMainWindow"

  private var preferencesChannel: FlutterMethodChannel?
  private var preferencesBridge: WorkbenchPreferencesBridge?

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
    "selectedConversationKey",
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
    guard let home = environment["HOME"],
          home.hasPrefix("/"),
          URL(fileURLWithPath: home, isDirectory: true).standardizedFileURL.path == home else {
      throw Failure.invalidState
    }
    let directory = URL(fileURLWithPath: home, isDirectory: true)
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
          let payload = object as? [String: Any],
          Set(payload.keys) == fields,
          payload["schema"] as? String == schema,
          let language = payload["language"] as? String,
          ["en-US", "zh-CN"].contains(language),
          let theme = payload["theme"] as? String,
          ["system", "light", "dark"].contains(theme),
          let section = payload["section"] as? String,
          ["captures", "conversations", "environments", "routes", "network", "settings"]
            .contains(section),
          validSelection(
            payload["selectedCaptureKey"],
            prefixes: ["managed_run:", "manual_capture:"]
          ),
          validSelection(
            payload["selectedConversationKey"],
            prefixes: ["capture_run:", "exchange:"]
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
