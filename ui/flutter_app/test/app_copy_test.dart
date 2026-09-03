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

  test('Chinese user-facing evidence terms stay plain and consistent', () {
    final chinese = AppCopy.forLanguage(AppLanguage.simplifiedChinese);

    expect(chinese('nav.captures'), '运行记录');
    expect(chinese('capture.empty'), '还没有运行记录。');
    expect(chinese('capture.session'), '查看客户端会话');
    expect(chinese('conversation.exchange'), 'Agent 调用');
    expect(chinese('exchange.attempt.one'), '1 次上游尝试');
    expect(chinese('capture.environment.apply_latest'), '下一轮应用');
  });
}
