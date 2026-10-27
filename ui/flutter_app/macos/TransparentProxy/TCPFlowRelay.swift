import Darwin
import Foundation

// A relay between one application flow and its broker connection.
protocol FlowRelay: AnyObject {
  func start(header: Data)
  func stop()
}

protocol ProxyTCPFlow: AnyObject {
  func open(completion: @escaping (Error?) -> Void)
  func read(completion: @escaping (Data?, Error?) -> Void)
  func write(_ data: Data, completion: @escaping (Error?) -> Void)
  func closeRead(error: Error?)
  func closeWrite(error: Error?)
}

final class TCPFlowRelay: FlowRelay {
  private let flow: ProxyTCPFlow
  private let queue = DispatchQueue(label: "io.vibermate.transparent-proxy.relay")
  private var socket: RelayDescriptor?
  private var reader: DispatchSourceRead?
  private var writer: DispatchSourceWrite?
  private var readerSuspended = true
  private var writerSuspended = true
  private var pending: Data?
  private var offset = 0
  private var afterWrite: (() -> Void)?
  private var onFinish: (() -> Void)?
  private var clientEnded = false
  private var brokerEnded = false
  private var started = false
  private var finished = false

  init(flow: ProxyTCPFlow, socket: BrokerSocket, onFinish: @escaping () -> Void) {
    self.flow = flow
    self.socket = RelayDescriptor(socket.takeDescriptor())
    self.onFinish = onFinish
  }

  func start(header: Data) {
    queue.async { [self] in
      guard !self.finished, !self.started, let socket = self.socket else { return }
      self.started = true
      let reader = DispatchSource.makeReadSource(fileDescriptor: socket.value, queue: self.queue)
      let writer = DispatchSource.makeWriteSource(fileDescriptor: socket.value, queue: self.queue)
      self.reader = reader
      self.writer = writer
      reader.setEventHandler { [weak self] in self?.readBroker() }
      writer.setEventHandler { [weak self] in self?.writeBroker() }
      // Each source retains the descriptor until cancellation has completed.
      // Closing earlier could make a delayed source use a recycled descriptor.
      reader.setCancelHandler { withExtendedLifetime(socket) {} }
      writer.setCancelHandler { withExtendedLifetime(socket) {} }
      self.send(header) {
        self.flow.open { [weak self] error in
          guard let self = self else { return }
          self.queue.async {
            guard !self.finished else { return }
            if let error = error { self.finish(error: error); return }
            self.resumeReader()
            self.readClient()
          }
        }
      }
    }
  }

  func stop() {
    queue.async { self.finish(error: NSError(domain: NSPOSIXErrorDomain, code: Int(ECANCELED))) }
  }

  private func send(_ data: Data, completion: @escaping () -> Void) {
    pending = data
    offset = 0
    afterWrite = completion
    if writerSuspended { writerSuspended = false; writer?.resume() }
  }

  private func writeBroker() {
    guard !finished, let socket = socket, let data = pending else { return }
    let count = data.withUnsafeBytes { bytes in
      Darwin.send(socket.value, bytes.baseAddress!.advanced(by: offset),
                  min(data.count - offset, 64 * 1024), 0)
    }
    if count < 0 {
      if errno != EINTR && errno != EAGAIN { failSocket() }
      return
    }
    if count == 0 { finish(error: FlowBrokerError.endOfStream); return }
    offset += count
    if offset == data.count {
      writer?.suspend()
      writerSuspended = true
      pending = nil
      let completion = afterWrite
      afterWrite = nil
      completion?()
    }
  }

  private func readClient() {
    guard !finished, !clientEnded else { return }
    flow.read { [weak self] data, error in
      guard let self = self else { return }
      self.queue.async {
        guard !self.finished else { return }
        if let error = error { self.finish(error: error); return }
        guard let data = data else { self.finish(error: FlowBrokerError.endOfStream); return }
        if data.isEmpty {
          self.clientEnded = true
          self.flow.closeRead(error: nil)
          if let socket = self.socket { shutdown(socket.value, SHUT_WR) }
          self.finishIfDrained()
        } else {
          // NEAppProxyTCPFlow does not expose a maximum read length. Retain
          // only this single platform chunk; request the next after it drains.
          self.send(data) { self.readClient() }
        }
      }
    }
  }

  private func readBroker() {
    guard !finished, !brokerEnded, let socket = socket else { return }
    var buffer = [UInt8](repeating: 0, count: 64 * 1024)
    let count = recv(socket.value, &buffer, buffer.count, 0)
    if count < 0 {
      if errno != EINTR && errno != EAGAIN { failSocket() }
      return
    }
    reader?.suspend()
    readerSuspended = true
    if count == 0 {
      brokerEnded = true
      flow.closeWrite(error: nil)
      finishIfDrained()
      return
    }
    flow.write(Data(buffer.prefix(count))) { [weak self] error in
      guard let self = self else { return }
      self.queue.async {
        guard !self.finished else { return }
        if let error = error { self.finish(error: error); return }
        self.resumeReader()
      }
    }
  }

  private func resumeReader() {
    if readerSuspended && !brokerEnded {
      readerSuspended = false
      reader?.resume()
    }
  }

  private func failSocket() {
    finish(error: NSError(domain: NSPOSIXErrorDomain, code: Int(errno)))
  }

  private func finishIfDrained() {
    if clientEnded && brokerEnded { finish(error: nil) }
  }

  private func finish(error: Error?) {
    guard !finished else { return }
    finished = true
    if !clientEnded { flow.closeRead(error: error) }
    if !brokerEnded { flow.closeWrite(error: error) }
    if let socket = socket { shutdown(socket.value, SHUT_RDWR) }
    if readerSuspended { reader?.resume() }
    if writerSuspended { writer?.resume() }
    reader?.cancel()
    writer?.cancel()
    reader = nil
    writer = nil
    socket = nil
    pending = nil
    afterWrite = nil
    let completion = onFinish
    onFinish = nil
    completion?()
  }
}

final class RelayDescriptor {
  let value: Int32
  init(_ value: Int32) { self.value = value }
  deinit { Darwin.close(value) }
}
