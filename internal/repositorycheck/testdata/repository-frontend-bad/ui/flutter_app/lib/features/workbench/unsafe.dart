import 'dart:ffi';
import 'dart:io';

import 'package:shared_preferences/shared_preferences.dart';

Future<void> acquireAuthority() async {
  HttpClient();
  await Process.start('unsafe', const []);
  await SharedPreferences.getInstance();
}
