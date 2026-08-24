import 'package:web/web.dart' as web;

const _key = 'io.vibermate.workbench.preferences.v1';

Future<Object?> readWorkbenchPreferences() async =>
    web.window.localStorage.getItem(_key);

Future<void> writeWorkbenchPreferences(String encoded) async {
  web.window.localStorage.setItem(_key, encoded);
}
