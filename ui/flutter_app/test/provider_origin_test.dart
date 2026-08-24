import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/provider_origin.dart';

void main() {
  test('provider origin accepts private HTTP and canonical HTTPS', () {
    expect(isCanonicalProviderOrigin('http://spark-2a59:8888'), isTrue);
    expect(isCanonicalProviderOrigin('http://127.0.0.1:8888'), isTrue);
    expect(isCanonicalProviderOrigin('http://169.254.10.20:8888'), isTrue);
    expect(isCanonicalProviderOrigin('http://192.168.50.4:8888'), isTrue);
    expect(isCanonicalProviderOrigin('http://100.64.0.1:8888'), isTrue);
    expect(isCanonicalProviderOrigin('http://[fd00::25]:8888'), isTrue);
    expect(isCanonicalProviderOrigin('http://[fe80::25]:8888'), isTrue);
    expect(isCanonicalProviderOrigin('http://[::1]:8888'), isTrue);
    expect(
      isCanonicalProviderOrigin('https://relay.example.com/api/provider'),
      isTrue,
    );
  });

  test('provider origin rejects ambiguous or unsafe spellings', () {
    expect(isCanonicalProviderOrigin('http://spark–2a59:8888'), isFalse);
    expect(isCanonicalProviderOrigin('http://8.8.8.8:8888'), isFalse);
    expect(isCanonicalProviderOrigin('http://203.0.113.2:8888'), isFalse);
    expect(isCanonicalProviderOrigin('http://999.1.1.1:8888'), isFalse);
    expect(
      isCanonicalProviderOrigin('http://[2001:4860:4860::8888]:8888'),
      isFalse,
    );
    expect(isCanonicalProviderOrigin('http://[fd00:::25]:8888'), isFalse);
    expect(isCanonicalProviderOrigin('https://relay.example.com:443'), isFalse);
    expect(isCanonicalProviderOrigin('https://relay.example.com/'), isFalse);
    expect(
      isCanonicalProviderOrigin('https://relay.example.com/a/../b'),
      isFalse,
    );
  });
}
