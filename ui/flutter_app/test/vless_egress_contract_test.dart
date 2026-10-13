import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';

void main() {
  test('VLESS description accepts only safe consistent security and trust', () {
    for (final wire in [
      {'security': 'reality'},
      {'security': 'tls', 'trustMode': 'system'},
      {'security': 'tls', 'trustMode': 'custom_ca'},
    ]) {
      expect(
        VlessProfileDescription.fromJson(wire, 'settings').security,
        wire['security'],
      );
    }
    for (final wire in [
      null,
      {},
      {'security': 'unknown'},
      {'security': 'tls'},
      {'security': 'tls', 'trustMode': null},
      {'security': 'tls', 'trustMode': 'skip'},
      {'security': 'reality', 'trustMode': 'system'},
      {'security': 'reality', 'trustMode': null},
      {'security': 'tls', 'trustMode': 'system', 'caPem': 'private'},
    ]) {
      expect(
        () => VlessProfileDescription.fromJson(wire, 'settings'),
        throwsA(isA<ControlContractException>()),
      );
    }
    expect(
      const VlessTLSImportOptions.customCA('private').toString(),
      isNot(contains('private')),
    );
  });
  final vless = <String, Object?>{
    'kind': 'vless',
    'credentialRef': 'secret://egress-vless/fixture',
    'credentialRevision': 1,
  };
  test('invalid credential pairing and domain JSON are refused', () {
    for (final proxy in [
      {...vless, 'kind': 'unknown'},
      {...vless, 'credentialRevision': 0},
      {...vless, 'credentialRevision': -1},
      {...vless, 'credentialRevision': 1.5},
      {...vless}..remove('credentialRevision'),
      {...vless}..remove('credentialRef'),
      {...vless, 'credentialRef': 'secret://other/fixture'},
      {...vless, 'credentialRef': 'secret://egress-vless/../fixture'},
      {...vless, 'endpoint': 'node.invalid:443'},
      {...vless, 'kind': 'direct'},
      {...vless, 'kind': 'socks5', 'endpoint': 'localhost:1080'},
    ]) {
      expect(
        () => TrafficProxyPolicy.fromJson(proxy, 'proxy'),
        throwsA(isA<ControlContractException>()),
      );
    }
    expect(
      () => TrafficEgressPolicy.fromJson({
        'proxy': vless,
        'resolver': {'kind': 'system', 'transport': 'direct'},
        'directDomains': ['example.com'],
      }, 'policy'),
      throwsA(isA<ControlContractException>()),
    );
  });
  test('old empty encoding and VLESS proxied resolver remain compatible', () {
    final old = const TrafficEgressPolicy.direct().toJson();
    expect(TrafficEgressPolicy.fromJson(old, 'policy').toJson(), old);
    expect(
      TrafficEgressPolicy.fromJson({
        ...old,
        'directDomains': '',
      }, 'policy').toJson(),
      old,
    );
    final wire = {
      'proxy': vless,
      'resolver': {
        'kind': 'doh',
        'dohUrl': 'https://1.1.1.1/dns-query',
        'transport': 'proxy',
      },
    };
    final policy = TrafficEgressPolicy.fromJson(wire, 'policy');
    expect(policy.toJson(), wire);
    expect(TrafficEgressPolicy.fromJson(wire, 'policy'), policy);
    expect(
      TrafficEgressPolicy.fromJson(wire, 'policy').hashCode,
      policy.hashCode,
    );
    expect(
      () => TrafficEgressPolicy.fromJson({
        ...wire,
        'proxy': {'kind': 'direct'},
      }, 'policy'),
      throwsA(isA<ControlContractException>()),
    );
  });
  test('saved VLESS policy roundtrips without exposing a node link', () {
    final wire = <String, Object?>{
      'proxy': {
        'kind': 'vless',
        'credentialRef': 'secret://egress-vless/fixture',
        'credentialRevision': 1,
      },
      'resolver': {'kind': 'system', 'transport': 'direct'},
      'directDomains': '*.github.com\ngithub.com',
    };
    final policy = TrafficEgressPolicy.fromJson(wire, 'policy');
    expect(policy.toJson(), wire);
  });
}
