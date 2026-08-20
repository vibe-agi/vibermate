import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/core/preferences/workbench_preferences.dart';
import 'package:vibermate_app/features/workbench/deletion_dialog.dart';

void main() {
  // The two answers are mutually exclusive at the source. A client that does
  // not enforce that would render "deleted" over a list of reasons it was not,
  // which is the one way this contract can mislead.
  test('a deletion outcome cannot be both done and refused', () {
    expect(
      () => DeletionOutcome.fromJson(const {
        'deleted': true,
        'holderCount': 2,
        'holders': <Object?>[],
      }, 'deletion'),
      throwsA(isA<ControlContractException>()),
    );
    expect(
      () => DeletionOutcome.fromJson(const {
        'deleted': false,
        'holderCount': 0,
        'holders': <Object?>[],
      }, 'deletion'),
      throwsA(isA<ControlContractException>()),
    );
  });

  // A truncated holder list that does not say it was truncated reads as the
  // whole story, and the user stops looking for the rest.
  test('a truncated holder list reports that it is truncated', () {
    final outcome = DeletionOutcome.fromJson(const {
      'deleted': false,
      'holderCount': 30,
      'holders': [
        {'kind': 'running_capture', 'id': 'run-1', 'label': 'claude'},
      ],
    }, 'deletion');
    expect(outcome.truncated, isTrue);
    expect(outcome.holderCount, 30);
    expect(
      () => DeletionOutcome.fromJson(const {
        'deleted': false,
        'holderCount': 1,
        'holders': [
          {'kind': 'running_capture', 'id': 'a', 'label': 'a'},
          {'kind': 'running_capture', 'id': 'b', 'label': 'b'},
        ],
      }, 'deletion'),
      throwsA(isA<ControlContractException>()),
    );
  });

  // The refusal path is the half that is easy to leave untested, and it is the
  // half that decides whether a blocked delete leaves the user with a move.
  testWidgets('a refused delete names its holders and stops asking again', (
    tester,
  ) async {
    var attempts = 0;
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: DeletionConfirmation(
          copy: AppCopy.forLanguage(AppLanguage.english),
          title: 'Clear the evidence archive?',
          subject: 'Evidence storage',
          consequence: 'Every Capture and all recorded evidence is removed.',
          onConfirm: () async {
            attempts++;
            return DeletionOutcome.fromJson(const {
              'deleted': false,
              'holderCount': 24,
              'holders': [
                {
                  'kind': 'running_capture',
                  'id': 'managed_run:run-1',
                  'label': 'claude',
                  'detail': 'attached',
                },
              ],
            }, 'deletion');
          },
        ),
      ),
    );
    await tester.pumpAndSettle();

    // The consequence is stated before the fact, not after.
    expect(
      find.textContaining('all recorded evidence is removed'),
      findsOneWidget,
    );

    await tester.tap(find.byKey(const Key('deletion-confirm')));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('deletion-blocked')), findsOneWidget);
    expect(
      find.byKey(const Key('deletion-holder-managed_run:run-1')),
      findsOneWidget,
    );
    // 24 held it and one was shown; a list that hides that reads as complete.
    expect(find.byKey(const Key('deletion-more-holders')), findsOneWidget);

    // Confirming again would ask the same question and get the same answer.
    expect(
      tester
          .widget<FilledButton>(find.byKey(const Key('deletion-confirm')))
          .onPressed,
      isNull,
    );
    expect(attempts, 1);
  });

  testWidgets('a completed delete closes and returns its receipt', (
    tester,
  ) async {
    DeletionOutcome? returned;
    await tester.pumpWidget(
      MaterialApp(
        theme: ViberTheme.light(),
        home: Builder(
          builder: (context) => ElevatedButton(
            onPressed: () async {
              returned = await showDialog<DeletionOutcome>(
                context: context,
                builder: (_) => DeletionConfirmation(
                  copy: AppCopy.forLanguage(AppLanguage.english),
                  title: 'Delete this Capture?',
                  subject: 'claude on agent-lab',
                  consequence: 'Every Exchange recorded under it is removed.',
                  onConfirm: () async => DeletionOutcome.fromJson(const {
                    'deleted': true,
                    'holderCount': 0,
                    'holders': <Object?>[],
                    'released': {
                      'exchanges': 24,
                      'envelopes': 96,
                      'activities': 24,
                      'connections': 8,
                      'attempts': 24,
                      'approvals': 3,
                      'assignments': 1,
                      'captures': 1,
                    },
                  }, 'deletion'),
                ),
              );
            },
            child: const Text('open'),
          ),
        ),
      ),
    );
    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('deletion-confirm')));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('deletion-confirm-dialog')), findsNothing);
    expect(returned?.deleted, isTrue);
    expect(returned?.released?.envelopes, 96);
    expect(returned?.released?.connections, 8);
    expect(returned?.released?.assignments, 1);
  });
}
