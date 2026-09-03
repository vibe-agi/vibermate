import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/bootstrap/runtime_connection.dart';

void main() {
  group('RuntimeServerLocation', () {
    test(
      'admits an explicit HTTP page origin without pretending it is TLS',
      () {
        final location = RuntimeServerLocation.fromPageUri(
          Uri.parse('http://192.168.1.20:9666/settings?tab=users'),
        );

        expect(location.baseUrl, Uri.parse('http://192.168.1.20:9666'));
        expect(location.displayLabel, 'http://192.168.1.20:9666');
        expect(location.encrypted, isFalse);
      },
    );

    test('admits an explicit HTTPS page origin as encrypted', () {
      final location = RuntimeServerLocation.fromPageUri(
        Uri.parse('https://runtime.example.net:9666/'),
      );

      expect(location.baseUrl, Uri.parse('https://runtime.example.net:9666'));
      expect(location.displayLabel, 'https://runtime.example.net:9666');
      expect(location.encrypted, isTrue);
    });

    test('rejects a non-HTTP management page origin', () {
      expect(
        () => RuntimeServerLocation.fromPageUri(
          Uri.parse('file:///tmp/vibermate/index.html'),
        ),
        throwsA(
          isA<RuntimeConnectionException>().having(
            (error) => error.message,
            'message',
            'server_transport_unsupported',
          ),
        ),
      );
    });

    test('rejects an origin containing user information', () {
      expect(
        () => RuntimeServerLocation.fromPageUri(
          Uri.parse('http://owner:secret@runtime.example.net:9666/'),
        ),
        throwsA(isA<RuntimeConnectionException>()),
      );
    });
  });
}
