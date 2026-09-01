import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  test('English and Chinese copy catalogs reject missing public keys', () {
    final english = AppCopy.forLanguage(AppLanguage.english);
    final chinese = AppCopy.forLanguage(AppLanguage.simplifiedChinese);

    expect(english('app.name'), 'ViberMate');
    expect(chinese('app.name'), 'ViberMate');
    expect(() => english('missing.public.key'), throwsAssertionError);
    expect(() => chinese('missing.public.key'), throwsAssertionError);
  });
}
