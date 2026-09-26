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
    expect(chinese('network.value.decryption.blind'), '内容未检查');
    expect(chinese('network.value.payload.opaque_tunnel'), '请求内容未检查');
  });

  test('blind egress describes inspection, not encryption', () {
    final english = AppCopy.forLanguage(AppLanguage.english);
    expect(
      english('network.value.purpose.blind_tunnel'),
      'Uninspected forwarding',
    );
    expect(
      english('network.value.payload.opaque_tunnel'),
      'Payload not inspected',
    );
  });

  test('transport diagnosis distinguishes known stages from unknown', () {
    for (final copy in [
      AppCopy.forLanguage(AppLanguage.english),
      AppCopy.forLanguage(AppLanguage.simplifiedChinese),
    ]) {
      final unknown = copy('exchange.failure.provider_transport_failed.title');
      for (final stage in [
        'dns',
        'tls',
        'profile',
        'handshake',
        'connection',
        'timeout',
      ]) {
        final prefix = 'exchange.failure.provider_transport_$stage';
        expect(copy('$prefix.title'), isNot(unknown));
        expect(copy('$prefix.action'), isNotEmpty);
      }
    }
    expect(
      AppCopy.forLanguage(AppLanguage.english)(
        'exchange.failure.provider_transport_failed.title',
      ),
      contains('unknown'),
    );
  });
}
