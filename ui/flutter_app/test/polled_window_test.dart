import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/features/workbench/workbench_controller.dart';

typedef _Item = ({String id, int at, String status});

void main() {
  String identity(_Item item) => item.id;
  Comparable<Object> recency(_Item item) => item.at;

  // The reported symptom: after a while inside a Capture the whole surface
  // stops answering clicks while the app itself stays alive. The polled window
  // was unioned into everything already held and kept, so a live Capture grew
  // this list every five seconds, and the workbench rebuilds all of it on every
  // controller notification.
  test('a live Capture cannot grow the retained window without limit', () {
    var held = <_Item>[];
    for (var poll = 0; poll < 200; poll++) {
      final latest = List.generate(
        100,
        (index) => (
          id: 'exchange-${poll * 100 + index}',
          at: poll * 100 + index,
          status: 'succeeded',
        ),
      );
      held = mergePolledWindow(
        held,
        latest,
        identity: identity,
        recency: recency,
      );
    }
    expect(held.length, retainedCaptureActivityLimit);
    // What survives is the newest, so the view a user is looking at is intact.
    expect(held.first.id, 'exchange-19999');
  });

  // The same merge decides what a second observation of one Exchange means.
  test('a repolled Activity takes its newer value', () {
    final held = mergePolledWindow(
      const [(id: 'exchange-1', at: 1, status: 'started')],
      const [(id: 'exchange-1', at: 2, status: 'succeeded')],
      identity: identity,
      recency: recency,
    );
    expect(held.single.status, 'succeeded');
  });

  // The controller orders by DateTime, so the comparison has to hold for the
  // type the production call site actually passes.
  test('recency ordering works for the timestamps the controller passes', () {
    final held = mergePolledWindow<({String id, DateTime at})>(
      [(id: 'older', at: DateTime.utc(2026, 8, 17, 3))],
      [(id: 'newer', at: DateTime.utc(2026, 8, 17, 4))],
      identity: (item) => item.id,
      recency: (item) => item.at,
    );
    expect(held.map((item) => item.id), ['newer', 'older']);
  });

  // Pages the user explicitly loaded are older than the polled window and must
  // not be dropped while they still fit.
  test('explicitly loaded history survives a poll', () {
    final loaded = List.generate(
      300,
      (index) => (id: 'old-$index', at: index, status: 'succeeded'),
    );
    final held = mergePolledWindow(
      loaded,
      const [(id: 'fresh', at: 10000, status: 'succeeded')],
      identity: identity,
      recency: recency,
    );
    expect(held.length, 301);
    expect(held.first.id, 'fresh');
    expect(held.any((item) => item.id == 'old-0'), isTrue);
  });
}
