import 'package:flutter/services.dart';

const _channel = MethodChannel('io.vibermate.desktop/preferences');

Future<Object?> readWorkbenchPreferences() =>
    _channel.invokeMethod<Object?>('readWorkbenchPreferences');

Future<void> writeWorkbenchPreferences(String encoded) =>
    _channel.invokeMethod<void>('writeWorkbenchPreferences', encoded);
