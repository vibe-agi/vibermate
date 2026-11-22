import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

void main() {
  test('upgrade failure retains plain localized data preservation copy', () {
    expect(
      AppCopy.forLanguage(AppLanguage.english)(
        'bootstrap.failure.storage_upgrade_incomplete',
      ),
      'Data upgrade incomplete; original data retained',
    );
    expect(
      AppCopy.forLanguage(AppLanguage.simplifiedChinese)(
        'bootstrap.failure.storage_upgrade_incomplete',
      ),
      '数据升级未完成，原数据保留',
    );
  });
  test('sentences from separate keys join without a space in Chinese', () {
    expect(
      AppCopy.forLanguage(
        AppLanguage.simplifiedChinese,
      ).sentences(['或更新 ViberMate。', '这台 Mac 上可以连接的版本：2.1.220。']),
      '或更新 ViberMate。这台 Mac 上可以连接的版本：2.1.220。',
    );
    expect(
      AppCopy.forLanguage(
        AppLanguage.english,
      ).sentences(['Update ViberMate.', 'Versions: 2.1.220.']),
      'Update ViberMate. Versions: 2.1.220.',
    );
  });

  test('a path in the guide never breaks at its slashes', () {
    for (final language in AppLanguage.values) {
      final detail = AppCopy.forLanguage(language)('setup.command.detail');
      // A word joiner after each slash keeps ~/.local/bin on one line.
      expect(detail, contains('~/\u2060.local/\u2060bin'));
      expect(detail.replaceAll('\u2060', ''), contains('~/.local/bin'));
    }
  });

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
    expect(chinese('capture.empty'), '还没有运行记录');
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
