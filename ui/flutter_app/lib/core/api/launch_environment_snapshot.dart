import 'dart:convert';

import 'control_models.dart';

/// Name-only observations of the actual CLI launcher's inherited environment.
/// Never infer values (or a browser's environment) from a server-side process.
final class LaunchEnvironmentSnapshot {
  const LaunchEnvironmentSnapshot({
    required this.id,
    required this.deviceName,
    required this.userLabel,
    required this.executable,
    required this.remote,
    required this.collectedAt,
    required this.names,
    this.truncated = false,
  });

  final String id, deviceName, userLabel, executable;
  final bool remote, truncated;
  final DateTime collectedAt;
  final List<String> names;

  static List<LaunchEnvironmentSnapshot> parseList(Object? json) {
    final response = requireObject(json, 'launchSnapshots');
    requireFields(response, 'launchSnapshots', required: const {'items'});
    final items = requireList(response['items'], 'launchSnapshots.items');
    if (items.length > 16) {
      throw const ControlContractException('too many launch snapshots');
    }
    return items
        .map((item) {
          const path = 'launchSnapshot';
          final value = requireObject(item, path);
          requireFields(
            value,
            path,
            required: const {
              'id',
              'deviceName',
              'userLabel',
              'executable',
              'remote',
              'collectedAt',
              'inventory',
            },
          );
          final inventory = requireObject(
            value['inventory'],
            '$path.inventory',
          );
          requireFields(
            inventory,
            '$path.inventory',
            required: const {'names', 'truncated'},
          );
          final names = requireList(inventory['names'], '$path.names');
          final strings = <String>[];
          var bytes = 0;
          for (final name in names) {
            if (name is! String ||
                !EnvironmentLaunchPolicy.validName(name) ||
                (strings.isNotEmpty && strings.last.compareTo(name) >= 0)) {
              throw const ControlContractException(
                'invalid launch environment names',
              );
            }
            bytes += utf8.encode(name).length;
            strings.add(name);
          }
          final timestamp = DateTime.tryParse(
            requireString(value, 'collectedAt', path),
          );
          if (strings.length > 256 ||
              bytes > 8192 ||
              timestamp == null ||
              value['remote'] is! bool ||
              inventory['truncated'] is! bool) {
            throw const ControlContractException('invalid launch snapshot');
          }
          return LaunchEnvironmentSnapshot(
            id: requireString(value, 'id', path),
        deviceName: requireStringValue(value, 'deviceName', path),
        userLabel: requireStringValue(value, 'userLabel', path),
            executable: requireString(value, 'executable', path),
            remote: value['remote']! as bool,
            collectedAt: timestamp,
            names: List.unmodifiable(strings),
            truncated: inventory['truncated']! as bool,
          );
        })
        .toList(growable: false);
  }
}
