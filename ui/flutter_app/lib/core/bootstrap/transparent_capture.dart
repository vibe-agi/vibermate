import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

/// Native capture belongs to this Mac, independently of the viewed Runtime.
/// Only the current generation and its public status remain in Dart memory.
final class TransparentCaptureController extends ChangeNotifier {
  TransparentCaptureController._();

  bool _listening = false;

  void _listen() {
    if (_listening) return;
    _channel.setMethodCallHandler((call) async {
      if (call.method == 'statusChanged') {
        _requestGeneration++;
        _applyStatus(call.arguments);
      }
    });
    _listening = true;
  }

  static final shared = TransparentCaptureController._();
  static const _channel = MethodChannel(
    'io.vibermate.desktop/transparent-capture',
  );
  static const _states = {
    'not_installed',
    'approval_required',
    'starting',
    'active',
    'disabled',
    'unavailable',
  };

  String _state = 'unavailable';
  String? _nativeError, _hostError;
  String? _instanceId, _configuredInstanceId;
  int _requestGeneration = 0;
  bool enabled = false;
  bool busy = false;

  String get state => _hostError == null ? _state : 'unavailable';
  String? get error => _hostError ?? _nativeError;
  bool get canEnable =>
      _hostError == null &&
      (_configuredInstanceId != null || state != 'unavailable');

  Future<void> configure({
    required String instanceId,
    int? flowPort,
    String? flowSecret,
    String? hostError,
  }) async {
    final previous = _configuredInstanceId;
    _instanceId = instanceId;
    _hostError = hostError;
    _requestGeneration++;
    if (flowPort != null && flowSecret != null) {
      _configuredInstanceId = instanceId;
      await _request('configure', {
        'instanceId': instanceId,
        'flowPort': flowPort,
        'flowSecret': flowSecret,
      });
    } else {
      _configuredInstanceId = null;
      if (previous != null) {
        await _request('clearConfiguration', {'instanceId': previous});
      }
    }
    notifyListeners();
  }

  Future<void> clearConfiguration(String instanceId) async {
    // An old daemon may finish closing after its replacement has connected.
    if (_instanceId != instanceId) return;
    _instanceId = null;
    _hostError = null;
    _requestGeneration++;
    if (_configuredInstanceId == instanceId) {
      _configuredInstanceId = null;
      await _request('clearConfiguration', {'instanceId': instanceId});
    }
    notifyListeners();
  }

  Future<void> refresh() async {
    if (!busy) await _request('status', null);
  }

  Future<void> setEnabled(bool value) async {
    if (busy) return;
    busy = true;
    notifyListeners();
    try {
      await _request('setEnabled', {'enabled': value});
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  Future<void> openSystemSettings() async {
    await _request('openSystemSettings', null, returnsStatus: false);
  }

  Future<void> _request(
    String method,
    Map<String, Object>? arguments, {
    bool returnsStatus = true,
  }) async {
    final generation = ++_requestGeneration;
    try {
      _listen();
    } on Error {
      // Process-only consumers can launch DesktopRuntime without a Flutter
      // binding. The optional bridge must not make their startup fail.
      _applyStatus(null);
      return;
    }
    Object? result;
    try {
      result = await _channel
          .invokeMethod<Object?>(method, arguments)
          .timeout(const Duration(seconds: 3));
      if (!returnsStatus) return;
    } on MissingPluginException {
      result = {'state': 'unavailable', 'enabled': false};
    } on PlatformException catch (failure) {
      result = {
        'state': 'unavailable',
        'enabled': enabled,
        'error': failure.message ?? failure.code,
      };
    } on TimeoutException {
      result = {'state': 'unavailable', 'enabled': enabled};
    }
    if (generation == _requestGeneration) _applyStatus(result);
  }

  void _applyStatus(Object? value) {
    if (value is Map &&
        _states.contains(value['state']) &&
        value['enabled'] is bool) {
      _state = value['state'] as String;
      enabled = value['enabled'] as bool;
      _nativeError = value['error'] is String ? value['error'] as String : null;
    } else {
      _state = 'unavailable';
      enabled = false;
      _nativeError = null;
    }
    notifyListeners();
  }
}
