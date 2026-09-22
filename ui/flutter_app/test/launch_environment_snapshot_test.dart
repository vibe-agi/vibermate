import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/api/launch_environment_snapshot.dart';

void main() {
  test(
    'snapshot contract accepts names, refuses value fields and duplicates',
    () {
      final inventory = <String, Object?>{
        'names': ['GH_TOKEN', 'PATH'],
        'truncated': false,
      };
      final json = {
        'items': [
          {
            'id': 'run-1',
            'deviceName': '',
            'userLabel': 'alice',
            'executable': 'codex',
            'remote': false,
            'collectedAt': '2026-09-22T12:00:00Z',
            'inventory': inventory,
          },
        ],
      };
      expect(LaunchEnvironmentSnapshot.parseList(json).single.names, [
        'GH_TOKEN',
        'PATH',
      ]);
      inventory['values'] = {'GH_TOKEN': 'private'};
      expect(
        () => LaunchEnvironmentSnapshot.parseList(json),
        throwsA(isA<ControlContractException>()),
      );
      inventory.remove('values');
      for (final names in [
        ['PATH', 'GH_TOKEN'],
        ['GH_TOKEN', 'GH_TOKEN'],
        ['GH_TOKEN=value'],
      ]) {
        inventory['names'] = names;
        expect(
          () => LaunchEnvironmentSnapshot.parseList(json),
          throwsA(isA<ControlContractException>()),
        );
      }
    },
  );
}
