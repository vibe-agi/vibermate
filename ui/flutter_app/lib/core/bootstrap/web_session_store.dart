import 'package:web/web.dart' as web;

/// Tab-scoped storage for the Web bearer session, separate from preferences.
abstract interface class WebSessionStore {
  String? read();
  void write(String encoded);
  void remove();
}

final class BrowserWebSessionStore implements WebSessionStore {
  const BrowserWebSessionStore();

  static const _key = 'vibermate.web-session.v1';

  @override
  String? read() => web.window.sessionStorage.getItem(_key);

  @override
  void write(String encoded) =>
      web.window.sessionStorage.setItem(_key, encoded);

  @override
  void remove() => web.window.sessionStorage.removeItem(_key);
}
