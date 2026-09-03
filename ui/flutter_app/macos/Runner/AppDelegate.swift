import Cocoa
import FlutterMacOS

@main
class AppDelegate: FlutterAppDelegate {
  override func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    return true
  }

  override func applicationSupportsSecureRestorableState(_ app: NSApplication) -> Bool {
    return true
  }

  override func applicationShouldTerminate(
    _ sender: NSApplication
  ) -> NSApplication.TerminateReply {
    do {
      for case let window as MainFlutterWindow in sender.windows {
        try window.prepareForApplicationTermination()
      }
      return .terminateNow
    } catch {
      // Preference persistence is part of a graceful Desktop shutdown. Refuse
      // the termination request if the last validated workbench state could
      // not be atomically committed; packaged acceptance treats this as a
      // bounded, visible failure instead of silently claiming a clean exit.
      return .terminateCancel
    }
  }
}
