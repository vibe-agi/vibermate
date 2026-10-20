import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter/material.dart';
import 'package:vibermate_app/core/api/account_facts_models.dart';
import 'package:vibermate_app/core/api/control_api.dart';
import 'package:vibermate_app/core/api/control_models.dart';
import 'package:vibermate_app/core/design/viber_theme.dart';
import 'package:vibermate_app/core/i18n/app_copy.dart';
import 'package:vibermate_app/features/workbench/provider_account_facts.dart';
import 'package:vibermate_app/features/workbench/provider_accounts_view.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';
import 'package:vibermate_app/preview/preview_control_api.dart';
import 'package:vibermate_app/preview/preview_terminal_command.dart';

void main() {
  final reviewDirectory = kIsWeb
      ? null
      : Platform.environment['VIBERMATE_UI_REVIEW_DIR'];
  if (reviewDirectory != null && Platform.isMacOS) {
    setUpAll(() async {
      for (final font in {
        viberSystemFontFamily: '/System/Library/Fonts/Helvetica.ttc',
        'Ahem': '/System/Library/Fonts/Helvetica.ttc',
        'Menlo': '/System/Library/Fonts/Menlo.ttc',
        'Hiragino Sans GB': '/System/Library/Fonts/Hiragino Sans GB.ttc',
      }.entries) {
        final loader = FontLoader(font.key)
          ..addFont(File(font.value).readAsBytes().then(ByteData.sublistView));
        await loader.load();
      }
      await (FontLoader(
        'MaterialIcons',
      )..addFont(rootBundle.load('fonts/MaterialIcons-Regular.otf'))).load();
    });
  }

  for (final language in [AppLanguage.english, AppLanguage.simplifiedChinese]) {
    for (final width in [390.0, 1180.0]) {
      testWidgets(
        'reset expiry countdown follows clock boundaries in $language at $width',
        (tester) async {
          await tester.binding.setSurfaceSize(Size(width, 960));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          var now = DateTime.utc(2031, 12, 31, 23, 59, 30);
          final api = _FactsApi()
            ..quota = {
              'rateLimitResets': {
                'availableCount': 4,
                'applicableAvailableCount': 4,
                'details': [
                  for (final (id, duration) in [
                    ('days', const Duration(days: 2, hours: 3)),
                    ('hours', const Duration(hours: 23, minutes: 59)),
                    ('minute', const Duration(minutes: 1)),
                    ('day-boundary', const Duration(days: 1)),
                    ('expired', const Duration(seconds: -1)),
                  ])
                    {
                      'id': id,
                      'title': 'Full reset · $id',
                      'description':
                          'Thanks for using Codex! You have been granted one free rate limit reset.',
                      'resetType': 'codex_rate_limits',
                      'status': 'available',
                      'grantedAt': now
                          .subtract(const Duration(days: 3))
                          .toIso8601String(),
                      'expiresAt': now.add(duration).toIso8601String(),
                    },
                ],
              },
            };
          final fixture = await _fixture(api, codexOAuth: true);
          final copy = AppCopy.forLanguage(language);
          final boundary = GlobalKey();
          final baseTheme = ViberTheme.dark();
          final theme = reviewDirectory == null
              ? baseTheme
              : baseTheme.copyWith(
                  textTheme: baseTheme.textTheme.apply(
                    fontFamily: viberSystemFontFamily,
                  ),
                  dialogTheme: baseTheme.dialogTheme.copyWith(
                    contentTextStyle: baseTheme.dialogTheme.contentTextStyle
                        ?.copyWith(fontFamily: viberSystemFontFamily),
                  ),
                );
          await fixture.controller.refreshProviderAccountQuota(fixture.account);
          await tester.pumpWidget(
            RepaintBoundary(
              key: boundary,
              child: MaterialApp(
                debugShowCheckedModeBanner: false,
                theme: theme,
                home: Scaffold(
                  body: Center(
                    child: ProviderAccountResetCreditsButton(
                      account: fixture.account,
                      controller: fixture.controller,
                      copy: copy,
                      clock: () => now,
                    ),
                  ),
                ),
              ),
            ),
          );
          await tester.tap(
            find.byKey(const Key('provider-account-resets-account.facts')),
          );
          await tester.pumpAndSettle();
          final english = language == AppLanguage.english;
          Finder entry(String id) =>
              find.byKey(Key('account-reset-credit-$id'));
          void countdown(String id, String en, String zh) {
            expect(
              find.descendant(
                of: entry(id),
                matching: find.text(english ? en : zh),
              ),
              findsOneWidget,
            );
          }

          countdown('days', 'Expires in 2 days 3 hours', '还有 2 天 3 小时到期');
          countdown(
            'hours',
            'Expires in 23 hours 59 minutes',
            '还有 23 小时 59 分钟到期',
          );
          countdown('minute', 'Expires in 1 minute', '还有 1 分钟到期');
          countdown('day-boundary', 'Expires in 1 day', '还有 1 天到期');
          countdown('expired', 'Expiration reached', '已到期');
          expect(
            find.textContaining('2032-01-03'),
            findsOneWidget,
            reason: 'The absolute expiry stays visible alongside the countdown',
          );
          expect(tester.widget<ListTile>(entry('expired')).onTap, isNull);
          expect(api.redeemCalls, isEmpty);
          await tester.ensureVisible(entry('minute'));
          await tester.tap(entry('minute'));
          await tester.pumpAndSettle();
          expect(
            tester
                .widget<FilledButton>(
                  find.byKey(const Key('account-reset-confirm')),
                )
                .onPressed,
            isNotNull,
          );
          now = now.add(const Duration(seconds: 1));
          await tester.pump(const Duration(seconds: 1));
          countdown('minute', 'Expires in less than a minute', '不足 1 分钟到期');
          countdown(
            'day-boundary',
            'Expires in 23 hours 59 minutes',
            '还有 23 小时 59 分钟到期',
          );
          now = now.add(const Duration(seconds: 59));
          await tester.pump(const Duration(seconds: 59));
          countdown('minute', 'Expiration reached', '已到期');
          expect(tester.widget<ListTile>(entry('minute')).onTap, isNull);
          expect(
            tester
                .widget<FilledButton>(
                  find.byKey(const Key('account-reset-confirm')),
                )
                .onPressed,
            isNull,
          );
          expect(
            api.redeemCalls,
            isEmpty,
            reason: 'Clock updates never redeem a reset',
          );
          tester.binding.handleAppLifecycleStateChanged(
            AppLifecycleState.paused,
          );
          now = now.add(const Duration(days: 4));
          tester.binding.handleAppLifecycleStateChanged(
            AppLifecycleState.resumed,
          );
          await tester.pump();
          countdown('days', 'Expiration reached', '已到期');
          expect(api.calls, [
            false,
          ], reason: 'Clock and resume updates do not re-read quota');
          await tester.tap(find.text(copy('common.cancel')));
          await tester.pumpAndSettle();
          now = DateTime.utc(2031, 12, 31, 23, 59, 30);
          final details =
              ((api.quota['rateLimitResets']!
                          as Map<String, Object>)['details']!
                      as List)
                  .whereType<Map<String, Object>>()
                  .where(
                    (item) => {'day-boundary', 'days'}.contains(item['id']),
                  )
                  .toList();
          api.quota = {
            'rateLimitResets': {
              'availableCount': 2,
              'applicableAvailableCount': 2,
              'details': details,
            },
          };
          await fixture.controller.refreshProviderAccountQuota(fixture.account);
          await tester.tap(
            find.byKey(const Key('provider-account-resets-account.facts')),
          );
          await tester.pumpAndSettle();
          await _reviewReset(
            tester,
            boundary,
            reviewDirectory,
            'reset-expiry-${width.toInt()}-${english ? 'en' : 'zh'}',
          );
          expect(tester.takeException(), isNull);
          await tester.tap(find.text(copy('common.cancel')));
          await tester.pumpAndSettle();
          await tester.pumpWidget(const SizedBox.shrink());
          fixture.controller.dispose();
        },
      );
    }
  }

  for (final detail in [false, true]) {
    testWidgets(
      '${detail ? 'detail' : 'summary'} manual quota wait follows credential replacement and disposal',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1180, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final requests = <Completer<http.Response>>[];
        final boundaryApi = await _quotaHttpApi((request) {
          final pending = Completer<http.Response>();
          requests.add(pending);
          return pending.future;
        });
        addTearDown(boundaryApi.close);
        var fixture = await _fixture(
          _FactsApi(),
          withInventory: true,
          controlApi: boundaryApi,
        );
        Future<void> mount() => detail
            ? _pumpPanel(tester, fixture)
            : _pumpSummary(tester, fixture);
        final oldAccount = fixture.account;
        final oldRead = fixture.controller.refreshProviderAccountQuota(
          oldAccount,
        );
        await mount();
        await tester.pump();
        final refresh = find.byKey(
          Key(
            detail
                ? 'account-quota-account.facts'
                : 'account-quota-refresh-account.facts',
          ),
        );
        final spinner = find.descendant(
          of: refresh,
          matching: find.byType(CircularProgressIndicator),
        );
        await tester.tap(refresh);
        await tester.pump();
        expect(spinner, findsOneWidget);
        final replacement = _nextCredential(oldAccount);
        final data = fixture.controller.data!;
        fixture.controller.data = DashboardData(
          status: data.status,
          captures: data.captures,
          captureNextCursor: data.captureNextCursor,
          environments: data.environments,
          endpoints: data.endpoints,
          accounts: [replacement],
        );
        fixture = (account: replacement, controller: fixture.controller);
        final newRead = fixture.controller.refreshProviderAccountQuota(
          replacement,
        );
        await mount();
        await tester.pump();
        expect(await oldRead, isNull);
        expect(spinner, findsNothing);
        await tester.tap(refresh);
        await tester.pump();
        expect(spinner, findsOneWidget);
        expect(requests, hasLength(2));
        requests[0].complete(_quotaHttpResponse(oldAccount, _alignmentQuota()));
        await tester.pump();
        expect(
          spinner,
          findsOneWidget,
          reason: 'An old response cannot stop a new scope wait',
        );
        requests[1].complete(
          _quotaHttpResponse(replacement, _alignmentQuota()),
        );
        await newRead;
        await tester.pumpAndSettle();
        expect(spinner, findsNothing);
        expect(
          fixture.controller.providerAccountQuota(replacement)?.credentialEpoch,
          2,
        );

        await tester.tap(refresh);
        await tester.pump();
        expect(requests, hasLength(3));
        expect(spinner, findsOneWidget);
        await tester.pumpWidget(const SizedBox.shrink());
        fixture.controller.dispose();
        requests[2].complete(
          _quotaHttpResponse(replacement, _alignmentQuota()),
        );
        await tester.pumpAndSettle();
        expect(tester.takeException(), isNull);
      },
    );

    for (final fails in [false, true]) {
      testWidgets(
        '${detail ? 'detail' : 'summary'} refresh joins a background quota read until ${fails ? 'failure' : 'completion'}',
        (tester) async {
          await tester.binding.setSurfaceSize(const Size(1180, 900));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          final pending = Completer<http.Response>();
          final requests = <http.Request>[];
          final boundaryApi = await _quotaHttpApi((request) {
            requests.add(request);
            return pending.future;
          });
          addTearDown(boundaryApi.close);
          final api = _FactsApi()..quota = _alignmentQuota();
          final fixture = await _fixture(
            api,
            withInventory: !detail,
            controlApi: boundaryApi,
          );
          final background = fixture.controller.refreshProviderAccountQuota(
            fixture.account,
          );
          if (detail) {
            await _pumpPanel(tester, fixture);
          } else {
            await _pumpSummary(tester, fixture);
          }
          await tester.pump();
          final refresh = find.byKey(
            Key(
              detail
                  ? 'account-quota-account.facts'
                  : 'account-quota-refresh-account.facts',
            ),
          );
          final spinner = find.descendant(
            of: refresh,
            matching: find.byType(CircularProgressIndicator),
          );
          expect(refresh, findsOneWidget);
          expect(
            spinner,
            findsNothing,
            reason: 'Background reads do not animate a refresh action',
          );
          expect(
            requests.map(
              (request) =>
                  '${request.method} ${request.url.path}?${request.url.query}',
            ),
            [
              'GET /api/v1/provider-accounts/account.facts/account-facts?kind=quota',
            ],
          );
          await tester.tap(refresh);
          await tester.pump();
          expect(spinner, findsOneWidget);
          await tester.tap(refresh);
          await tester.pump(const Duration(seconds: 1));
          expect(
            spinner,
            findsOneWidget,
            reason: 'Joining must await the actual pending read',
          );
          expect(
            requests,
            hasLength(1),
            reason: 'Manual joins never issue a second HTTP request',
          );
          if (fails) {
            pending.completeError(StateError('synthetic quota failure'));
          } else {
            pending.complete(_quotaHttpResponse(fixture.account, api.quota));
          }
          await background;
          await tester.pumpAndSettle();
          expect(spinner, findsNothing);
          expect(
            fixture.controller.providerAccountQuotaFailed(fixture.account),
            fails,
          );
          expect(tester.takeException(), isNull);
          await tester.pumpWidget(const SizedBox.shrink());
          fixture.controller.dispose();
        },
      );
    }
  }

  for (final detail in [false, true]) {
    testWidgets(
      'quota metadata centers align in real ${detail ? 'detail' : 'summary'}',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1180, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = _FactsApi()..quota = _alignmentQuota();
        final fixture = await _fixture(
          api,
          codexOAuth: true,
          withInventory: !detail,
        );
        try {
          if (detail) {
            await _pumpPanel(tester, fixture);
            await _query(tester);
          } else {
            await fixture.controller.ensureProviderAccountQuotas([
              fixture.account,
            ]);
            await _pumpSummary(tester, fixture);
            await tester.pumpAndSettle();
          }
          _expectQuotaAlignment(
            tester,
            detail: detail,
            balance: '62500.00',
            count: 2,
            sameRun: true,
          );
        } finally {
          await tester.pumpWidget(const SizedBox.shrink());
          fixture.controller.dispose();
        }
      },
    );
  }
  for (final detail in [false, true]) {
    for (final config in [
      (width: 390.0, scale: 1.0, dark: true, english: false, parent: null),
      (width: 1180.0, scale: 1.6, dark: false, english: true, parent: null),
      (width: 390.0, scale: 1.6, dark: false, english: false, parent: null),
      (width: 1180.0, scale: 2.0, dark: true, english: false, parent: null),
      (width: 390.0, scale: 2.0, dark: true, english: true, parent: null),
      (width: 1180.0, scale: 2.0, dark: false, english: false, parent: 320.0),
    ]) {
      testWidgets(
        'quota metadata responsive ${detail ? 'detail' : 'summary'} $config',
        (tester) async {
          await tester.binding.setSurfaceSize(Size(config.width, 900));
          addTearDown(() => tester.binding.setSurfaceSize(null));
          final two = config.scale != 1.6;
          final balance = two ? '62429.25' : '62500.00';
          final count = two ? 1 : 2;
          final api = _FactsApi()
            ..quota = _alignmentQuota(
              balance: balance,
              count: count,
              twoWindows: two,
            );
          final fixture = await _fixture(
            api,
            codexOAuth: true,
            withInventory: !detail,
          );
          final language = config.english
              ? AppLanguage.english
              : AppLanguage.simplifiedChinese;
          try {
            if (detail) {
              await _pumpPanel(
                tester,
                fixture,
                dark: config.dark,
                scale: config.scale,
                language: language,
                parentWidth: config.parent,
              );
              await _query(tester);
            } else {
              await fixture.controller.ensureProviderAccountQuotas([
                fixture.account,
              ]);
              await _pumpSummary(
                tester,
                fixture,
                dark: config.dark,
                scale: config.scale,
                language: language,
                parentWidth: config.parent,
              );
              await tester.pumpAndSettle();
            }
            if (!detail &&
                config.width == 390 &&
                config.english &&
                config.scale == 2) {
              _logAccountActionBounds(tester);
            }
            _expectQuotaAlignment(
              tester,
              detail: detail,
              balance: balance,
              count: count,
              sameRun: config.parent == null ? null : false,
            );
            final root = find.byKey(
              Key(
                detail
                    ? 'account-facts-quota'
                    : 'provider-account-quota-summary-account.facts',
              ),
            );
            expect(
              find.descendant(
                of: root,
                matching: find.byType(LinearProgressIndicator),
              ),
              findsNWidgets(two ? 2 : 1),
            );
          } finally {
            await tester.pumpWidget(const SizedBox.shrink());
            fixture.controller.dispose();
          }
        },
      );
    }
  }
  for (final detail in [false, true]) {
    testWidgets(
      'quota metadata loading refresh and absence ${detail ? 'detail' : 'summary'}',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1180, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final initial = Completer<AccountFacts>();
        final api = _FactsApi()
          ..quota = _alignmentQuota()
          ..pendingQuota = initial.future;
        final fixture = await _fixture(
          api,
          codexOAuth: true,
          withInventory: !detail,
        );
        try {
          if (detail) {
            await _pumpPanel(tester, fixture);
            await _startQuotaRefresh(tester, fixture, detail: true);
          } else {
            await _pumpSummary(tester, fixture);
            await tester.pump();
          }
          expect(
            fixture.controller.providerAccountQuotaLoading(fixture.account),
            isTrue,
          );
          expect(find.textContaining('62500.00'), findsNothing);
          expect(
            find.byKey(const Key('provider-account-resets-account.facts')),
            findsNothing,
          );
          expect(find.byType(CircularProgressIndicator), findsWidgets);
          expect(tester.takeException(), isNull);
          initial.complete(api.facts(false));
          api.pendingQuota = null;
          await tester.pumpAndSettle();
          _expectQuotaAlignment(
            tester,
            detail: detail,
            balance: '62500.00',
            count: 2,
            sameRun: true,
          );

          final refresh = Completer<AccountFacts>();
          api.pendingQuota = refresh.future;
          await _startQuotaRefresh(tester, fixture, detail: detail);
          expect(
            fixture.controller.providerAccountQuotaLoading(fixture.account),
            isTrue,
          );
          _expectQuotaAlignment(
            tester,
            detail: detail,
            balance: '62500.00',
            count: 2,
            sameRun: true,
          );
          api.quota = _alignmentQuota(
            balance: '62429.25',
            count: 1,
            twoWindows: true,
          );
          refresh.complete(api.facts(false));
          api.pendingQuota = null;
          await tester.pumpAndSettle();
          _expectQuotaAlignment(
            tester,
            detail: detail,
            balance: '62429.25',
            count: 1,
            sameRun: true,
          );

          Future<void> replace(Map<String, Object> quota) async {
            api.quota = quota;
            await _startQuotaRefresh(tester, fixture, detail: detail);
            await tester.pumpAndSettle();
            expect(
              fixture.controller.providerAccountQuotaLoading(fixture.account),
              isFalse,
            );
            expect(tester.takeException(), isNull);
          }

          final root = find.byKey(
            Key(
              detail
                  ? 'account-facts-quota'
                  : 'provider-account-quota-summary-account.facts',
            ),
          );
          final voucher = find.byKey(
            const Key('provider-account-resets-account.facts'),
          );
          await replace(_alignmentQuota(count: null));
          expect(voucher, findsNothing);
          final absentHeight = tester.getSize(root).height;
          await replace(_alignmentQuota());
          _expectQuotaAlignment(
            tester,
            detail: detail,
            balance: '62500.00',
            count: 2,
            sameRun: true,
          );
          await replace(_alignmentQuota(count: null));
          expect(voucher, findsNothing);
          expect(tester.getSize(root).height, closeTo(absentHeight, .5));
          await replace(_alignmentQuota(count: 0));
          _expectQuotaAlignment(
            tester,
            detail: detail,
            balance: '62500.00',
            count: 0,
            sameRun: true,
          );
          expect(tester.widget<TextButton>(voucher).onPressed, isNotNull);
          await replace(_alignmentQuota(balance: null));
          expect(
            find.descendant(
              of: root,
              matching: find.textContaining('62500.00'),
            ),
            findsNothing,
          );
          expect(
            find.descendant(of: voucher, matching: find.text('2')),
            findsOneWidget,
          );
          await replace(_alignmentQuota(balance: null, count: null));
          expect(voucher, findsNothing);
          expect(tester.getSize(root).height, lessThan(absentHeight));
          expect(api.redeemCalls, isEmpty);
        } finally {
          await tester.pumpWidget(const SizedBox.shrink());
          fixture.controller.dispose();
        }
      },
    );

    testWidgets(
      'quota aligned voucher keeps explicit confirmation ${detail ? 'detail' : 'summary'}',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1180, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = _FactsApi()
          ..quota = {
            ..._alignmentQuota(),
            'rateLimitResets': {
              'availableCount': 2,
              'applicableAvailableCount': 2,
              'details': [
                for (final id in ['first', 'second'])
                  {
                    'id': id,
                    'title': id,
                    'resetType': 'codex_rate_limits',
                    'status': 'available',
                    'grantedAt': '2026-09-01T00:00:00Z',
                  },
              ],
            },
          };
        final fixture = await _fixture(
          api,
          codexOAuth: true,
          withInventory: !detail,
        );
        try {
          if (detail) {
            await _pumpPanel(tester, fixture);
            await _query(tester);
          } else {
            await fixture.controller.ensureProviderAccountQuotas([
              fixture.account,
            ]);
            await _pumpSummary(tester, fixture);
            await tester.pumpAndSettle();
          }
          _expectQuotaAlignment(
            tester,
            detail: detail,
            balance: '62500.00',
            count: 2,
            sameRun: true,
          );
          await tester.tap(
            find.byKey(const Key('provider-account-resets-account.facts')),
          );
          await tester.pumpAndSettle();
          expect(find.byType(AlertDialog), findsOneWidget);
          expect(api.redeemCalls, isEmpty);
          await tester.tap(find.byKey(const Key('account-reset-credit-first')));
          await tester.pumpAndSettle();
          expect(
            find.byKey(const Key('account-reset-confirm')),
            findsOneWidget,
          );
          expect(api.redeemCalls, isEmpty);
          await tester.tap(find.byKey(const Key('account-reset-confirm')));
          await tester.pumpAndSettle();
          expect(api.redeemCalls.single.creditId, 'first');
          expect(find.byType(AlertDialog), findsNothing);
          expect(tester.takeException(), isNull);
        } finally {
          await tester.pumpWidget(const SizedBox.shrink());
          fixture.controller.dispose();
        }
      },
    );
  }
  test('credits have two decimals and only live vouchers expire soon', () {
    expect(accountCreditsLabel('62429.2476250000', 'unknown'), '62429.25');
    expect(accountCreditsLabel('0', 'unknown'), '0.00');
    expect(accountCreditsLabel(null, 'unknown'), 'unknown');
    final now = DateTime.utc(2026, 9, 30);
    for (final (hours, soon) in [
      (-1, false),
      (0, false),
      (1, true),
      (72, true),
      (73, false),
    ]) {
      final credit = AccountResetCredit.fromJson({
        'id': 'fixture',
        'resetType': 'codex_rate_limits',
        'status': 'available',
        'grantedAt': now.subtract(const Duration(days: 10)).toIso8601String(),
        'expiresAt': now.add(Duration(hours: hours)).toIso8601String(),
      });
      expect(resetCreditExpiresSoon(credit, now), soon);
    }
  });

  testWidgets('voucher details combine all warnings in one confirmation', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1180, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final now = DateTime.now().toUtc();
    final api = _FactsApi()
      ..quota = {
        'limits': [
          {
            'id': 'codex',
            'primary': {
              ..._window(41),
              'resetAt':
                  now.add(const Duration(hours: 2)).millisecondsSinceEpoch ~/
                  1000,
            },
          },
        ],
        'rateLimitResets': {
          'availableCount': 2,
          'applicableAvailableCount': 2,
          'details': [
            for (final (id, days) in [
              ('expired', -1),
              ('used', -1),
              ('earliest', 1),
              ('later', 2),
            ])
              {
                'id': id,
                'title': id,
                'description': 'Fixture voucher details',
                'resetType': 'codex_rate_limits',
                'status': id == 'used' ? 'redeemed' : 'available',
                'grantedAt': now
                    .subtract(const Duration(days: 10))
                    .toIso8601String(),
                'expiresAt': now.add(Duration(days: days)).toIso8601String(),
              },
          ],
        },
      };
    final fixture = await _fixture(api, codexOAuth: true);
    addTearDown(fixture.controller.dispose);
    await _pumpPanel(tester, fixture);
    await _query(tester);
    final chip = find.byKey(const Key('provider-account-resets-account.facts'));
    expect(
      tester.widget<TextButton>(chip).style!.foregroundColor!.resolve({}),
      ViberColors.dark.danger,
    );
    await tester.ensureVisible(chip);
    await tester.tap(chip);
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsOneWidget);
    expect(api.redeemCalls, isEmpty);
    expect(
      find.descendant(
        of: find.byKey(const Key('account-reset-credit-used')),
        matching: find.text('已使用'),
      ),
      findsOneWidget,
    );
    expect(
      tester
          .widget<ListTile>(
            find.byKey(const Key('account-reset-credit-expired')),
          )
          .onTap,
      isNull,
    );
    final later = find.byKey(const Key('account-reset-credit-later'));
    await tester.ensureVisible(later);
    await tester.tap(later);
    await tester.pumpAndSettle();
    final warning = tester
        .widget<Text>(
          find.descendant(
            of: find.byKey(const Key('account-reset-warnings')),
            matching: find.byType(Text),
          ),
        )
        .data!;
    expect(warning, contains('本周已使用 41%'));
    expect(warning, contains('更早到期'));
    expect(warning, contains('3 小时内自然重置'));
    expect(find.byType(AlertDialog), findsOneWidget);
    expect(api.redeemCalls, isEmpty);
    await tester.tap(find.byKey(const Key('account-reset-confirm')));
    await tester.pumpAndSettle();
    expect(api.redeemCalls.single.creditId, 'later');
    expect(find.byType(AlertDialog), findsNothing);
    expect(tester.takeException(), isNull);
  });
  for (final width in [390.0, 1180.0]) {
    testWidgets(
      'account quota is explicit and remains separate from history at $width',
      (tester) async {
        await tester.binding.setSurfaceSize(Size(width, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        final api = PreviewControlApi(seedCaptures: false);
        final account = await api.createProviderAccount(
          id: 'account.facts',
          displayName: 'Managed B',
          upstreamEndpointId: 'target.codex.official',
          kind: 'bearer_token',
          secret: 'synthetic-secret',
          unlinked: true,
          headerPolicy: const ProviderAccountHeaderPolicy(),
        );
        final controller = WorkbenchController(
          api: api,
          terminalCommands: PreviewTerminalCommandService(),
          previewMode: true,
          closeRuntime: api.close,
        );
        addTearDown(controller.dispose);
        await tester.pumpWidget(
          MaterialApp(
            theme: ViberTheme.dark(),
            home: Scaffold(
              body: SingleChildScrollView(
                child: ProviderAccountFactsPanel(
                  account: account,
                  controller: controller,
                  copy: AppCopy.forLanguage(AppLanguage.simplifiedChinese),
                ),
              ),
            ),
          ),
        );
        expect(find.textContaining('25%'), findsNothing);
        await tester.tap(find.byKey(const Key('account-quota-account.facts')));
        await tester.pumpAndSettle();
        expect(find.textContaining('25%'), findsOneWidget);
        expect(find.byTooltip('Codex 可用额度重置券：1 · 查看重置券详情'), findsOneWidget);
        expect(find.text('当前可使用：1'), findsOneWidget);
        expect(
          tester.getSize(find.byType(LinearProgressIndicator).first).width,
          lessThanOrEqualTo(width < 600 ? 340 : 300),
        );
        expect(find.textContaining('1,200'), findsNothing);
        await tester.tap(
          find.byKey(const Key('account-history-account.facts')),
        );
        await tester.pumpAndSettle();
        expect(find.text('1.20K'), findsOneWidget);
        expect(find.text('1,200 tokens'), findsOneWidget);
        final quota = tester.getRect(
          find.byKey(const Key('account-facts-quota')),
        );
        final history = tester.getRect(
          find.byKey(const Key('account-facts-history')),
        );
        expect(history.top, greaterThanOrEqualTo(quota.bottom));
        expect(find.text('synthetic-secret'), findsNothing);
        expect(tester.takeException(), isNull);
      },
    );
  }
  testWidgets('large totals stay readable and exact in both themes', (
    tester,
  ) async {
    final api = _FactsApi();
    final fixture = await _fixture(api);
    addTearDown(fixture.controller.dispose);
    for (final dark in [true, false]) {
      await _pumpPanel(tester, fixture, dark: dark);
      await _query(tester);
      await _query(tester, history: true);
      expect(find.text('Pro'), findsNothing);
      expect(find.text('6.98B'), findsOneWidget);
      expect(find.text('6,980,410,315 tokens'), findsOneWidget);
      expect(find.text('codex'), findsNothing);
      expect(find.text('积分余额：无可用积分'), findsOneWidget);
      expect(find.text('上游统计截至 2026-09-21'), findsOneWidget);
      expect(
        tester
            .widget<LinearProgressIndicator>(
              find.byType(LinearProgressIndicator),
            )
            .value,
        .69,
      );
      expect(tester.takeException(), isNull);
    }
  });

  testWidgets('failed reads only mark their own retained observation', (
    tester,
  ) async {
    final api = _FactsApi();
    final fixture = await _fixture(api);
    addTearDown(fixture.controller.dispose);
    await _pumpPanel(tester, fixture);
    expect(api.calls, isEmpty);
    await _query(tester);
    expect(api.calls, [false]);
    await _query(tester, history: true);
    api.failQuota = true;
    await _query(tester);
    final stale = find.text('刷新失败 · 已保留上次成功采集的数据。');
    final quota = find.byKey(const Key('account-facts-quota'));
    final history = find.byKey(const Key('account-facts-history'));
    expect(find.descendant(of: quota, matching: stale), findsOneWidget);
    expect(find.descendant(of: history, matching: stale), findsNothing);
    expect(find.text('6,980,410,315 tokens'), findsOneWidget);
    expect(find.textContaining('69%'), findsOneWidget);
    // A successful history read must not clear a failed quota refresh.
    await _query(tester, history: true);
    expect(find.descendant(of: quota, matching: stale), findsOneWidget);
    api.failQuota = false;
    api.failHistory = true;
    await _query(tester);
    await _query(tester, history: true);
    expect(find.descendant(of: quota, matching: stale), findsNothing);
    expect(find.descendant(of: history, matching: stale), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('quota snapshot survives panel rebuild until explicit refresh', (
    tester,
  ) async {
    final api = _FactsApi();
    final fixture = await _fixture(api);
    addTearDown(fixture.controller.dispose);
    await _pumpPanel(tester, fixture);
    await _query(tester);
    expect(api.calls, [false]);

    await _pumpPanel(tester, fixture);
    await tester.pumpAndSettle();
    expect(find.textContaining('69%'), findsOneWidget);
    expect(api.calls, [false]);

    await _query(tester);
    expect(api.calls, [false, false]);
    expect(tester.takeException(), isNull);
  });

  testWidgets('missing data is not zero, and partial history is explicit', (
    tester,
  ) async {
    final api = _FactsApi()
      ..quota = {
        'limits': <Object>[],
        'credits': {'hasCredits': true, 'unlimited': false},
      }
      ..history = {
        'history': {'daily': <Object>[], 'partial': true},
      };
    final fixture = await _fixture(api);
    addTearDown(fixture.controller.dispose);
    await _pumpPanel(tester, fixture);
    await _query(tester);
    await _query(tester, history: true);
    expect(find.byType(LinearProgressIndicator), findsNothing);
    expect(find.text('上游未提供额度窗口，不代表用量为 0%。'), findsOneWidget);
    expect(find.text('积分余额：未提供'), findsOneWidget);
    expect(find.text('—'), findsOneWidget);
    expect(find.text('未提供'), findsOneWidget);
    expect(find.text('上游报告历史统计暂不完整。'), findsOneWidget);
    expect(find.text('0 tokens'), findsNothing);
    expect(find.text('单日最高 Token'), findsNothing);
    expect(find.text('连续活跃天数'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('zero, exhausted quota and exact credits are not lost', (
    tester,
  ) async {
    final api = _FactsApi()
      ..quota = {
        'limits': [
          {
            'id': 'codex',
            'allowed': false,
            'limitReached': true,
            'primary': _window(0),
            'secondary': _window(105),
          },
        ],
        'credits': {'hasCredits': true, 'unlimited': false, 'balance': '12.50'},
      }
      ..history = {
        'history': {'lifetimeTokens': 0, 'daily': <Object>[], 'partial': false},
      };
    final fixture = await _fixture(api);
    addTearDown(fixture.controller.dispose);
    await _pumpPanel(tester, fixture);
    await _query(tester);
    await _query(tester, history: true);
    expect(find.text('0 tokens'), findsOneWidget);
    expect(find.text('已达限额'), findsOneWidget);
    expect(find.textContaining('105%'), findsOneWidget);
    expect(find.text('积分余额：12.50'), findsOneWidget);
    expect(
      tester
          .widgetList<LinearProgressIndicator>(
            find.byType(LinearProgressIndicator),
          )
          .map((w) => w.value),
      [0, 1],
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('narrow panel supports larger text without overflow', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(390, 1400));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = await _fixture(_FactsApi());
    addTearDown(fixture.controller.dispose);
    await _pumpPanel(tester, fixture, scale: 1.6);
    await _query(tester);
    await _query(tester, history: true);
    expect(find.text('6,980,410,315 tokens'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'banked reset requires choosing a credit and explicit confirmation',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(390, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final api = _FactsApi()
        ..quota = {
          'planType': 'pro',
          'limits': [
            {'id': 'codex', 'allowed': false, 'primary': _window(100)},
          ],
          'rateLimitResets': {
            'availableCount': 1,
            'applicableAvailableCount': 1,
            'details': [
              {
                'id': 'credit-fixture',
                'resetType': 'codex_rate_limits',
                'status': 'available',
                'grantedAt': '2026-09-01T00:00:00Z',
                'expiresAt': '2030-10-01T00:00:00Z',
                'title': 'Full reset',
              },
            ],
          },
        };
      final fixture = await _fixture(api, codexOAuth: true);
      addTearDown(fixture.controller.dispose);
      await _pumpPanel(tester, fixture);
      await _query(tester);
      final reset = find.byKey(
        const Key('provider-account-resets-account.facts'),
      );
      expect(reset, findsOneWidget);
      await tester.ensureVisible(reset);
      await tester.tap(reset);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Full reset').last);
      await tester.pumpAndSettle();
      expect(find.text('使用这张重置券？'), findsOneWidget);
      await tester.tap(find.text('取消').last);
      await tester.pumpAndSettle();
      expect(api.redeemCalls, isEmpty);
      await tester.tap(reset);
      await tester.pumpAndSettle();
      await tester.tap(find.text('Full reset').last);
      await tester.pumpAndSettle();
      final pending = Completer<AccountResetRedemption>();
      api.pendingRedeem = pending.future;
      await tester.tap(find.text('确认使用'));
      await tester.pump();
      expect(api.redeemCalls, hasLength(1));
      expect(tester.widget<TextButton>(reset).onPressed, isNull);
      pending.complete(
        AccountResetRedemption.fromJson({
          'accountId': fixture.account.id,
          'credentialEpoch': fixture.account.credentialEpoch,
          'creditId': 'credit-fixture',
          'outcome': 'reset',
          'windowsReset': 2,
        }),
      );
      await tester.pumpAndSettle();
      expect(api.redeemCalls, hasLength(1));
      expect(api.redeemCalls.single.creditId, 'credit-fixture');
      expect(find.text('已使用重置券。'), findsOneWidget);
      expect(api.calls.where((history) => !history).length, 2);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'ambiguous reset requires a fresh quota read before an explicit retry',
    (tester) async {
      final api = _FactsApi()
        ..quota = {
          'limits': <Object>[],
          'rateLimitResets': {
            'availableCount': 1,
            'details': [
              {
                'id': 'credit-fixture',
                'resetType': 'codex_rate_limits',
                'status': 'available',
                'grantedAt': '2026-09-01T00:00:00Z',
                'expiresAt': '2030-10-01T00:00:00Z',
              },
            ],
          },
        }
        ..resetUnconfirmed = true;
      final fixture = await _fixture(api, codexOAuth: true);
      addTearDown(fixture.controller.dispose);
      await _pumpPanel(tester, fixture);
      await _query(tester);
      Future<void> confirm() async {
        final reset = find.byKey(
          const Key('provider-account-resets-account.facts'),
        );
        await tester.ensureVisible(reset);
        await tester.tap(reset);
        await tester.pumpAndSettle();
        await tester.tap(find.text('Codex').last);
        await tester.pumpAndSettle();
        await tester.tap(find.text('确认使用'));
        await tester.pumpAndSettle();
      }

      await confirm();
      expect(find.text('未能确认重置结果。请先查看最新额度，再决定是否重试。'), findsOneWidget);
      expect(
        find.byKey(const Key('provider-account-resets-account.facts')),
        findsNothing,
      );
      await _query(tester);
      api.resetUnconfirmed = false;
      await confirm();
      expect(api.redeemCalls, hasLength(2));
      expect(api.redeemCalls[1].creditId, api.redeemCalls[0].creditId);
      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'pending quota does not block history and resets on credential change',
    (tester) async {
      final api = _FactsApi();
      final fixture = await _fixture(api);
      addTearDown(fixture.controller.dispose);
      final pending = Completer<AccountFacts>();
      api.pendingQuota = pending.future;
      await _pumpPanel(tester, fixture);
      await tester.tap(find.byKey(const Key('account-quota-account.facts')));
      await tester.pump();
      await tester.tap(find.byKey(const Key('account-history-account.facts')));
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 200));
      expect(api.calls, [false, true]);
      expect(find.text('6.98B'), findsOneWidget);
      final next = await _fixture(_FactsApi(), id: 'account.next');
      addTearDown(next.controller.dispose);
      await _pumpPanel(tester, next);
      pending.complete(api.facts(false));
      await tester.pumpAndSettle();
      expect(find.textContaining('69%'), findsNothing);
      expect(find.text('6.98B'), findsNothing);
      expect(tester.takeException(), isNull);
    },
  );

  test('account facts preserve source, missing windows and exact balances', () {
    final input = <String, Object?>{
      'accountId': 'account.b',
      'credentialEpoch': 2,
      'origin': 'https://chatgpt.com',
      'adapterId': 'chatgpt-codex',
      'adapterRevision': 1,
      'observedAt': '2026-09-22T01:00:00Z',
      'state': 'known',
      'planType': 'pro',
      'limits': <Object?>[],
    };
    final missing = AccountFacts.fromJson(input);
    expect(missing.accountId, 'account.b');
    expect(missing.credentialEpoch, 2);
    expect(missing.limits, isEmpty);
    expect(missing.credits, isNull);
    expect(missing.rateLimitResets, isNull);
    final zero = AccountFacts.fromJson({
      ...input,
      'limits': [
        {
          'id': 'codex',
          'allowed': true,
          'limitReached': false,
          'primary': {
            'usedPercent': 0,
            'windowSeconds': 18000,
            'resetAfterSeconds': 3600,
            'resetAt': 1800000000,
          },
        },
      ],
      'credits': {'hasCredits': true, 'unlimited': false, 'balance': '12.50'},
      'rateLimitResets': {'availableCount': 2, 'applicableAvailableCount': 1},
    });
    expect(zero.limits.single.primary?.usedPercent, 0);
    expect(zero.limits.single.secondary, isNull);
    expect(zero.credits?.balance, '12.50');
    expect(zero.rateLimitResets?.availableCount, 2);
    expect(zero.rateLimitResets?.applicableAvailableCount, 1);
    expect(
      AccountFacts.fromJson({
        ...input,
        'rateLimitResets': {'availableCount': 0},
      }).rateLimitResets?.applicableAvailableCount,
      isNull,
    );
    expect(
      () => AccountFacts.fromJson({
        ...input,
        'rateLimitResets': {'availableCount': 0, 'applicableAvailableCount': 1},
      }),
      throwsA(isA<ControlContractException>()),
    );
    expect(
      () => AccountFacts.fromJson({...input, 'accessToken': 'nope'}),
      throwsA(isA<ControlContractException>()),
    );
  });
}

typedef _Fixture = ({ProviderAccount account, WorkbenchController controller});

ProviderAccount _nextCredential(ProviderAccount account) => ProviderAccount(
  id: account.id,
  displayName: account.displayName,
  credentialOrigin: account.credentialOrigin,
  linkedEndpointIds: account.linkedEndpointIds,
  associationRevision: account.associationRevision,
  kind: account.kind,
  realmId: account.realmId,
  state: account.state,
  revision: account.revision + 1,
  credentialState: account.credentialState,
  credentialEpoch: account.credentialEpoch + 1,
  setHeaderNames: account.setHeaderNames,
  deleteHeaderNames: account.deleteHeaderNames,
  tokenInfo: account.tokenInfo,
  codexOAuth: account.codexOAuth,
  settingsRevision: account.settingsRevision,
  note: account.note,
  noteRevision: account.noteRevision,
  automaticRefresh: account.automaticRefresh,
  cyberAccessProgram: account.cyberAccessProgram,
  supportsAutomaticRefresh: account.supportsAutomaticRefresh,
  egressProfile: account.egressProfile,
);

Future<HttpControlApi> _quotaHttpApi(
  Future<http.Response> Function(http.Request) handler,
) => HttpControlApi.connect(
  DesktopSession(
    baseUrl: Uri.parse('http://127.0.0.1:54999'),
    readToken: 'R' * 43,
    writeToken: 'W' * 43,
    instanceId: 'synthetic-account-quota',
    expiresAt: DateTime.now().toUtc().add(const Duration(days: 1)),
  ),
  client: MockClient(handler),
  inspectSession: false,
);

http.Response _quotaHttpResponse(
  ProviderAccount account,
  Map<String, Object> quota,
) => http.Response(
  jsonEncode({
    'accountId': account.id,
    'credentialEpoch': account.credentialEpoch,
    'origin': account.credentialOrigin,
    'adapterId': 'chatgpt-codex',
    'adapterRevision': 1,
    'observedAt': '2026-10-20T00:00:00Z',
    'state': 'known',
    'limits': <Object>[],
    ...quota,
  }),
  200,
  headers: {'content-type': 'application/json'},
);

Future<void> _reviewReset(
  WidgetTester tester,
  GlobalKey key,
  String? directory,
  String name,
) async {
  if (directory == null) return;
  await tester.runAsync(() async {
    final boundary =
        key.currentContext!.findRenderObject()! as RenderRepaintBoundary;
    final image = await boundary.toImage();
    final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
    await Directory(directory).create(recursive: true);
    await File(
      '$directory/$name.png',
    ).writeAsBytes(bytes!.buffer.asUint8List());
    image.dispose();
  });
}

Future<_Fixture> _fixture(
  _FactsApi api, {
  String id = 'account.facts',
  bool codexOAuth = false,
  bool withInventory = false,
  ControlApi? controlApi,
}) async {
  final preview = PreviewControlApi(seedCaptures: false);
  final claims = base64Url
      .encode(
        utf8.encode(
          jsonEncode({
            'exp':
                DateTime.now()
                    .toUtc()
                    .add(const Duration(days: 1))
                    .millisecondsSinceEpoch ~/
                1000,
            'https://api.openai.com/auth': {
              'chatgpt_account_id': 'workspace-fixture',
            },
          }),
        ),
      )
      .replaceAll('=', '');
  final token = 'eyJhbGciOiJub25lIn0.$claims.fixture';
  final account = await preview.createProviderAccount(
    id: id,
    displayName: 'Managed B',
    upstreamEndpointId: 'target.codex.official',
    kind: codexOAuth ? 'codex_oauth' : 'bearer_token',
    secret: codexOAuth ? '' : 'synthetic-secret',
    codexAuthJson: codexOAuth
        ? jsonEncode({
            'auth_mode': 'chatgpt',
            'OPENAI_API_KEY': null,
            'tokens': {
              'access_token': token,
              'id_token': token,
              'refresh_token': 'synthetic-refresh',
              'account_id': 'workspace-fixture',
            },
            'last_refresh': DateTime.now().toUtc().toIso8601String(),
          })
        : '',
    unlinked: true,
    headerPolicy: const ProviderAccountHeaderPolicy(),
  );
  api.account = account;
  final controller = WorkbenchController(
    api: controlApi ?? api,
    terminalCommands: PreviewTerminalCommandService(),
    previewMode: !codexOAuth,
    closeRuntime: preview.close,
  );
  if (withInventory) {
    final dashboard = await preview.loadDashboard();
    controller.data = DashboardData(
      status: dashboard.status,
      captures: dashboard.captures,
      captureNextCursor: dashboard.captureNextCursor,
      environments: dashboard.environments,
      endpoints: dashboard.endpoints,
      accounts: [account],
    );
  }
  return (account: account, controller: controller);
}

Map<String, Object> _alignmentQuota({
  String? balance = '62500.00',
  int? count = 2,
  bool twoWindows = false,
}) => {
  'limits': [
    {
      'id': 'codex',
      'primary': _window(41),
      if (twoWindows) 'secondary': {..._window(20), 'windowSeconds': 18000},
    },
  ],
  if (balance != null)
    'credits': {'hasCredits': true, 'unlimited': false, 'balance': balance},
  if (count != null)
    'rateLimitResets': {
      'availableCount': count,
      'applicableAvailableCount': count,
    },
};

Future<void> _pumpSummary(
  WidgetTester tester,
  _Fixture fixture, {
  bool dark = true,
  double scale = 1,
  AppLanguage language = AppLanguage.simplifiedChinese,
  double? parentWidth,
}) => tester.pumpWidget(
  MaterialApp(
    theme: dark ? ViberTheme.dark() : ViberTheme.light(),
    builder: (context, child) => MediaQuery(
      data: MediaQuery.of(
        context,
      ).copyWith(textScaler: TextScaler.linear(scale)),
      child: child!,
    ),
    home: Scaffold(
      body: Align(
        alignment: Alignment.topLeft,
        child: SizedBox(
          width: parentWidth,
          child: AnimatedBuilder(
            animation: fixture.controller,
            builder: (context, _) => ProviderAccountsView(
              controller: fixture.controller,
              copy: AppCopy.forLanguage(language),
            ),
          ),
        ),
      ),
    ),
  ),
);

void _logAccountActionBounds(WidgetTester tester) {
  final refresh = find.byKey(const Key('provider-accounts-refresh-all'));
  late Rect headerRect;
  tester.element(refresh).visitAncestorElements((element) {
    if (element.widget is LayoutBuilder) {
      final box = element.findRenderObject()! as RenderBox;
      headerRect = tester.getRect(
        find.byElementPredicate((candidate) => identical(candidate, element)),
      );
      debugPrint(
        'header local LayoutBuilder constraints=${box.constraints} maxWidthFinite=${box.constraints.maxWidth.isFinite}',
      );
      return false;
    }
    return true;
  });
  for (final key in [
    'provider-accounts-refresh-all',
    'provider-accounts-add',
  ]) {
    final button = find.byKey(Key(key));
    final label = find.descendant(of: button, matching: find.byType(Text));
    final buttonRect = tester.getRect(button),
        labelRect = tester.getRect(label);
    debugPrint(
      '$key type=${tester.widget(button).runtimeType} rect=${tester.getRect(button)} label=${tester.getRect(label)}',
    );
    expect(buttonRect.left, greaterThanOrEqualTo(headerRect.left));
    expect(buttonRect.right, lessThanOrEqualTo(headerRect.right));
    expect(buttonRect.height, greaterThanOrEqualTo(30));
    expect(labelRect.left, greaterThanOrEqualTo(buttonRect.left));
    expect(labelRect.right, lessThanOrEqualTo(buttonRect.right));
    expect(labelRect.top, greaterThanOrEqualTo(buttonRect.top));
    expect(labelRect.bottom, lessThanOrEqualTo(buttonRect.bottom));
  }
  final hit = find.byKey(
    const Key('account-automatic-refresh-hit-account.facts'),
  );
  final row = find.ancestor(of: hit, matching: find.byType(Row)).first;
  final label = find.descendant(of: row, matching: find.byType(Text));
  debugPrint(
    'automatic-refresh row constraints=${tester.renderObject<RenderBox>(row).constraints} row=${tester.getRect(row)} label=${tester.getRect(label)} hit=${tester.getRect(hit)}',
  );
  final rowRect = tester.getRect(row),
      hitRect = tester.getRect(hit),
      labelRect = tester.getRect(label);
  expect(hitRect.size, const Size(56, 44));
  expect(hitRect.right, lessThanOrEqualTo(rowRect.right));
  expect(labelRect.right, lessThanOrEqualTo(hitRect.left));
  expect(labelRect.top, greaterThanOrEqualTo(rowRect.top));
  expect(labelRect.bottom, lessThanOrEqualTo(rowRect.bottom));
}

void _expectQuotaAlignment(
  WidgetTester tester, {
  required bool detail,
  required String balance,
  required int count,
  bool? sameRun,
}) {
  final root = find.byKey(
    Key(
      detail
          ? 'account-facts-quota'
          : 'provider-account-quota-summary-account.facts',
    ),
  );
  final credits = find.descendant(
    of: root,
    matching: find.textContaining(balance),
  );
  final button = find.descendant(
    of: root,
    matching: find.byKey(const Key('provider-account-resets-account.facts')),
  );
  final voucher = find.descendant(of: button, matching: find.text('$count'));
  expect(credits, findsOneWidget);
  expect(voucher, findsOneWidget);
  final creditRect = tester.getRect(credits),
      voucherRect = tester.getRect(voucher);
  final buttonRect = tester.getRect(button), rootRect = tester.getRect(root);
  final onSameRun =
      creditRect.top < buttonRect.bottom && buttonRect.top < creditRect.bottom;
  if (sameRun != null) {
    expect(onSameRun, sameRun, reason: 'Intended natural metadata run');
  }
  expect(buttonRect.width, greaterThanOrEqualTo(32));
  expect(buttonRect.height, greaterThanOrEqualTo(28));
  for (final rect in [creditRect, buttonRect]) {
    expect(rect.left, greaterThanOrEqualTo(rootRect.left - .5));
    expect(rect.right, lessThanOrEqualTo(rootRect.right + .5));
    expect(rect.top, greaterThanOrEqualTo(rootRect.top - .5));
    expect(rect.bottom, lessThanOrEqualTo(rootRect.bottom + .5));
  }
  final voucherIcon = tester.getRect(
    find.descendant(
      of: button,
      matching: find.byIcon(Icons.confirmation_number_outlined),
    ),
  );
  debugPrint(
    'quota ${detail ? 'detail' : 'summary'} sameRun=$onSameRun credits=$creditRect voucher=$voucherRect voucherIcon=$voucherIcon',
  );
  final creditIcon = detail
      ? null
      : tester.getRect(
          find.descendant(of: root, matching: find.byIcon(Icons.toll_outlined)),
        );
  if (creditIcon != null) {
    debugPrint(
      'quota summary creditIcon=$creditIcon iconCenterDelta=${(creditIcon.center.dy - voucherIcon.center.dy).abs()}',
    );
  }
  expect(tester.takeException(), isNull);
  if (onSameRun) {
    expect(voucherRect.left, greaterThan(creditRect.right));
    expect(
      (creditRect.center.dy - voucherRect.center.dy).abs(),
      lessThanOrEqualTo(.5),
      reason: 'Actual metadata content centers must align on one run',
    );
    if (!detail) {
      expect(
        (creditIcon!.center.dy - voucherIcon.center.dy).abs(),
        lessThanOrEqualTo(.5),
      );
    }
  } else {
    expect(buttonRect.top, greaterThanOrEqualTo(creditRect.bottom));
    expect(buttonRect.overlaps(creditRect), isFalse);
  }
}

Future<void> _pumpPanel(
  WidgetTester tester,
  _Fixture fixture, {
  bool dark = true,
  double scale = 1,
  AppLanguage language = AppLanguage.simplifiedChinese,
  double? parentWidth,
}) {
  final panel = ProviderAccountFactsPanel(
    account: fixture.account,
    controller: fixture.controller,
    copy: AppCopy.forLanguage(language),
  );
  return tester.pumpWidget(
    MaterialApp(
      theme: dark ? ViberTheme.dark() : ViberTheme.light(),
      home: MediaQuery(
        data: MediaQueryData(textScaler: TextScaler.linear(scale)),
        child: Scaffold(
          body: SingleChildScrollView(
            child: parentWidth == null
                ? panel
                : Align(
                    alignment: Alignment.topLeft,
                    child: SizedBox(width: parentWidth, child: panel),
                  ),
          ),
        ),
      ),
    ),
  );
}

Future<void> _startQuotaRefresh(
  WidgetTester tester,
  _Fixture fixture, {
  required bool detail,
}) async {
  if (detail) {
    await tester.tap(find.byKey(const Key('account-quota-account.facts')));
  } else {
    unawaited(fixture.controller.refreshProviderAccountQuota(fixture.account));
  }
  await tester.pump();
}

Future<void> _query(WidgetTester tester, {bool history = false}) async {
  final button = find.byKey(
    Key('account-${history ? 'history' : 'quota'}-account.facts'),
  );
  await tester.ensureVisible(button);
  await tester.tap(button);
  await tester.pumpAndSettle();
}

Map<String, Object> _window(int used) => {
  'usedPercent': used,
  'windowSeconds': 604800,
  'resetAfterSeconds': 180000,
  'resetAt': DateTime(2026, 9, 24, 19, 42).millisecondsSinceEpoch ~/ 1000,
};

final class _FactsApi extends Fake implements ControlApi {
  late ProviderAccount account;
  final calls = <bool>[];
  bool failQuota = false, failHistory = false;
  Future<AccountFacts>? pendingQuota;
  Future<AccountResetRedemption>? pendingRedeem;
  bool resetUnconfirmed = false;
  final redeemCalls = <({String creditId})>[];
  Map<String, Object> quota = {
    'planType': 'pro',
    'limits': [
      {'id': 'codex', 'allowed': true, 'primary': _window(69)},
    ],
    'credits': {'hasCredits': false, 'unlimited': false, 'balance': '0'},
  };
  Map<String, Object> history = {
    'history': {
      'lifetimeTokens': 6980410315,
      'asOf': '2026-09-21',
      'daily': <Object>[],
      'partial': false,
    },
  };

  AccountFacts facts(bool isHistory) => AccountFacts.fromJson({
    'accountId': account.id,
    'credentialEpoch': account.credentialEpoch,
    'origin': account.credentialOrigin,
    'adapterId': 'chatgpt-codex',
    'adapterRevision': 1,
    'observedAt': '2026-09-22T04:56:07Z',
    'state': 'known',
    'limits': <Object>[],
    ...(isHistory ? history : quota),
  });

  @override
  Future<AccountFacts> accountFacts(String id, {bool history = false}) async {
    calls.add(history);
    if (history ? failHistory : failQuota) {
      throw StateError('synthetic failure');
    }
    if (!history && pendingQuota != null) return pendingQuota!;
    return facts(history);
  }

  @override
  Future<AccountResetRedemption> redeemAccountResetCredit(
    ProviderAccount account,
    String creditId,
  ) async {
    redeemCalls.add((creditId: creditId));
    if (pendingRedeem != null) return pendingRedeem!;
    if (resetUnconfirmed) {
      throw const ControlProblem(
        status: 502,
        reasonCode: 'reset_result_unconfirmed',
        messageKey: 'error.reset_result_unconfirmed',
      );
    }
    return AccountResetRedemption.fromJson({
      'accountId': account.id,
      'credentialEpoch': account.credentialEpoch,
      'creditId': creditId,
      'outcome': 'reset',
      'windowsReset': 2,
    });
  }
}
