import 'package:flutter_test/flutter_test.dart';
import 'package:vibermate_app/features/workbench/capture_conversation_tree.dart';

void main() {
  test('uses exact native parent IDs and keeps creation order', () {
    final origin = DateTime.utc(2026, 8, 14, 11, 30);
    CaptureConversationTreeSeed<String> seed({
      required String value,
      required int second,
      String? actor,
      List<String> parents = const [],
      String status = 'succeeded',
    }) => CaptureConversationTreeSeed<String>(
      value: value,
      key: value,
      client: actor == null ? null : 'claude',
      sessionId: actor == null ? null : 'session-1',
      actorId: actor,
      parentActorIds: parents,
      firstObservedAt: origin.add(Duration(seconds: second)),
      status: status,
    );

    final tree = buildCaptureConversationTree<String>([
      seed(value: 'child-b', second: 3, actor: 'b', parents: const ['root']),
      seed(value: 'unrelated', second: 1),
      seed(value: 'grandchild', second: 4, actor: 'g', parents: const ['a']),
      seed(value: 'root', second: 0, actor: 'root'),
      seed(
        value: 'child-a',
        second: 2,
        actor: 'a',
        parents: const ['root'],
        status: 'pending',
      ),
    ]);

    expect(tree.map((entry) => entry.value), [
      'root',
      'child-a',
      'grandchild',
      'child-b',
      'unrelated',
    ]);
    expect(tree.map((entry) => entry.depth), [0, 1, 2, 1, 0]);
    expect(tree.map((entry) => entry.ancestorHasNextSibling), [
      const <bool>[],
      const <bool>[],
      const <bool>[true],
      const <bool>[],
      const <bool>[],
    ]);
    expect(tree.first.status, 'pending');
    expect(tree.first.hasChildren, isTrue);
  });

  test('does not relate actors across sessions or invent missing parents', () {
    final time = DateTime.utc(2026, 8, 14);
    final tree = buildCaptureConversationTree<String>([
      CaptureConversationTreeSeed<String>(
        value: 'root-other-session',
        key: 'root-other-session',
        client: 'claude',
        sessionId: 'session-2',
        actorId: 'root',
        parentActorIds: const [],
        firstObservedAt: time,
        status: 'succeeded',
      ),
      CaptureConversationTreeSeed<String>(
        value: 'orphan',
        key: 'orphan',
        client: 'claude',
        sessionId: 'session-1',
        actorId: 'child',
        parentActorIds: const ['root'],
        firstObservedAt: time.add(const Duration(seconds: 1)),
        status: 'failed',
      ),
    ]);

    expect(tree.map((entry) => entry.depth), [0, 0]);
    expect(tree.last.status, 'failed');
  });
}
