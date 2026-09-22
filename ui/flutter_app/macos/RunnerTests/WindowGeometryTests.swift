import Cocoa
import XCTest
#if !WINDOW_GEOMETRY_STANDALONE
@testable import ViberMate
#endif

@objcMembers
final class WindowGeometryTests: XCTestCase {
  private let chrome = NSSize(width: 0, height: 28)
  private let desktop = NSRect(x: 0, y: 48, width: 1920, height: 1100)

  func testFirstLaunchUses1280By800ContentAndCenters() {
    let frame = WorkbenchWindowGeometry.recommendedFrame(in: desktop, chrome: chrome)
    XCTAssertEqual(frame.width, 1280)
    XCTAssertEqual(frame.height - chrome.height, 800)
    XCTAssertEqual(frame.midX, desktop.midX)
    XCTAssertEqual(frame.midY, desktop.midY)
  }

  func testShortScreenKeepsWidthAndLeavesVerticalDesktopSpace() {
    let screen = NSRect(x: 0, y: 40, width: 1366, height: 690)
    let frame = WorkbenchWindowGeometry.recommendedFrame(in: screen, chrome: chrome)
    XCTAssertEqual(frame.width, screen.width * 0.85, accuracy: 0.0001)
    XCTAssertEqual(frame.height, screen.height * 0.8, accuracy: 0.0001)
    XCTAssertGreaterThan(frame.width / (frame.height - chrome.height), 1.6)
    XCTAssertEqual(frame.midX, screen.midX)
    XCTAssertEqual(frame.midY, screen.midY)
  }

  func testNarrowScreenDoesNotCreateAnUnnecessarilyTallWindow() {
    let screen = NSRect(x: 0, y: 40, width: 900, height: 1400)
    let frame = WorkbenchWindowGeometry.recommendedFrame(in: screen, chrome: chrome)
    XCTAssertEqual(frame.width, 765)
    XCTAssertEqual(frame.width / (frame.height - chrome.height), 1.6, accuracy: 0.0001)
    XCTAssertTrue(screen.contains(frame))
  }

  func testExternalScreenCentersWithinItsOwnCoordinates() {
    let screen = NSRect(x: -2560, y: 120, width: 2560, height: 1360)
    let frame = WorkbenchWindowGeometry.recommendedFrame(in: screen, chrome: chrome)
    XCTAssertEqual(frame.width, 1280)
    XCTAssertEqual(frame.midX, screen.midX)
    XCTAssertEqual(frame.midY, screen.midY)
    XCTAssertTrue(screen.contains(frame))
  }

  func testRestorationPreservesCustomSquareAndNarrowFrames() {
    for saved in [
      NSRect(x: 100, y: 80, width: 850, height: 850),
      NSRect(x: 300, y: 100, width: 390, height: 720),
      desktop,
    ] {
      XCTAssertEqual(WorkbenchWindowGeometry.fittedFrame(
        saved, visibleFrames: [desktop], fallback: desktop
      ), saved)
    }
  }

  func testDisconnectedDisplayReturnsWindowToAvailableScreen() {
    let saved = NSRect(x: -2400, y: 100, width: 1280, height: 828)
    let fitted = WorkbenchWindowGeometry.fittedFrame(
      saved, visibleFrames: [desktop], fallback: desktop
    )
    XCTAssertEqual(fitted.size, saved.size)
    XCTAssertTrue(desktop.contains(fitted))
    XCTAssertEqual(fitted.minX, desktop.minX)
  }

  func testLargestOverlapKeepsWindowOnItsExistingDisplay() {
    let secondary = NSRect(x: 1920, y: 48, width: 1440, height: 850)
    let saved = NSRect(x: 1800, y: 100, width: 1000, height: 700)
    let fitted = WorkbenchWindowGeometry.fittedFrame(
      saved, visibleFrames: [desktop, secondary], fallback: desktop
    )
    XCTAssertTrue(secondary.contains(fitted))
    XCTAssertEqual(fitted.size, saved.size)
  }

  func testOversizedRestoredFrameFitsVisibleAreaWithoutAspectLock() {
    let screen = NSRect(x: 0, y: 48, width: 1024, height: 700)
    let fitted = WorkbenchWindowGeometry.fittedFrame(
      NSRect(x: -20, y: -40, width: 1600, height: 1500),
      visibleFrames: [screen], fallback: screen
    )
    XCTAssertEqual(fitted, screen)
  }

  func testEmptyScreenSnapshotStillUsesKnownFallback() {
    let fitted = WorkbenchWindowGeometry.fittedFrame(
      NSRect(x: 10000, y: 10000, width: 900, height: 700),
      visibleFrames: [], fallback: desktop
    )
    XCTAssertTrue(desktop.contains(fitted))
  }
}

// Run the exact production geometry without launching Flutter or the live
// runtime: bash ui/flutter_app/tool/test_window_geometry.sh
#if WINDOW_GEOMETRY_STANDALONE
@main
enum WindowGeometryTestRunner {
  static func main() {
    let suite = XCTestSuite(forTestCaseClass: WindowGeometryTests.self)
    suite.run()
    guard let result = suite.testRun else { exit(1) }
    print("Window geometry: \(result.executionCount) tests, \(result.totalFailureCount) failures")
    exit(result.executionCount == 9 && result.totalFailureCount == 0 ? 0 : 1)
  }
}
#endif
