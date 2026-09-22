import Cocoa

/// Window placement in logical points. Only first launch/reset uses the
/// recommended proportions; a user's restored frame is never ratio-locked.
enum WorkbenchWindowGeometry {
  static let recommendedContentSize = NSSize(width: 1280, height: 800)
  static let minimumFrameSize = NSSize(width: 390, height: 620)

  static func recommendedFrame(in visibleFrame: NSRect, chrome: NSSize) -> NSRect {
    let contentWidth = max(0, min(
      recommendedContentSize.width, visibleFrame.width * 0.85 - chrome.width
    ))
    // A shorter screen should reduce height, not also squeeze the directory
    // and evidence panes. Narrow screens still scale the reference height.
    let contentHeight = max(0, min(
      contentWidth * recommendedContentSize.height / recommendedContentSize.width,
      visibleFrame.height * 0.8 - chrome.height
    ))
    let size = NSSize(
      width: contentWidth + chrome.width,
      height: contentHeight + chrome.height
    )
    return NSRect(
      x: visibleFrame.midX - size.width / 2,
      y: visibleFrame.midY - size.height / 2,
      width: size.width,
      height: size.height
    )
  }

  static func fittedFrame(
    _ frame: NSRect,
    visibleFrames: [NSRect],
    fallback: NSRect
  ) -> NSRect {
    // Preserve the exact saved frame whenever it still fits a connected screen.
    if visibleFrames.contains(where: { $0.contains(frame) }) { return frame }
    var target = fallback
    var largestOverlap: CGFloat = 0
    for candidate in visibleFrames {
      let intersection = candidate.intersection(frame)
      let overlap = intersection.isNull ? 0 : intersection.width * intersection.height
      if overlap > largestOverlap {
        target = candidate
        largestOverlap = overlap
      }
    }
    let width = min(frame.width, target.width)
    let height = min(frame.height, target.height)
    return NSRect(
      x: min(max(frame.minX, target.minX), target.maxX - width),
      y: min(max(frame.minY, target.minY), target.maxY - height),
      width: width,
      height: height
    )
  }
}
