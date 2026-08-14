/// A client-neutral seed for the Capture conversation directory.
///
/// Native Agent identifiers stay opaque. The caller supplies the exact actor
/// and parent actor identifiers advertised by Claude, Codex, or a future
/// client. This module only establishes a stable presentation tree; it never
/// invents a relationship from timestamps or titles.
final class CaptureConversationTreeSeed<T> {
  const CaptureConversationTreeSeed({
    required this.value,
    required this.key,
    required this.client,
    required this.sessionId,
    required this.actorId,
    required this.parentActorIds,
    required this.firstObservedAt,
    required this.status,
  });

  final T value;
  final String key;
  final String? client;
  final String? sessionId;
  final String? actorId;
  final List<String> parentActorIds;
  final DateTime firstObservedAt;
  final String status;
}

final class CaptureConversationTreeEntry<T> {
  const CaptureConversationTreeEntry({
    required this.value,
    required this.key,
    required this.parentKey,
    required this.depth,
    required this.hasChildren,
    required this.isLastSibling,
    required this.ancestorHasNextSibling,
    required this.status,
  });

  final T value;
  final String key;
  final String? parentKey;
  final int depth;
  final bool hasChildren;
  final bool isLastSibling;

  /// Whether each visible ancestor (excluding the root and direct parent)
  /// has a following sibling. The directory uses this exact traversal state
  /// to draw stable tree guides without guessing relationships in the UI.
  final List<bool> ancestorHasNextSibling;

  /// The strongest state in this branch. A running descendant keeps its
  /// parent visibly active; otherwise failure outranks cancellation/success.
  final String status;
}

List<CaptureConversationTreeEntry<T>> buildCaptureConversationTree<T>(
  Iterable<CaptureConversationTreeSeed<T>> values,
) {
  final seeds = values.toList(growable: false)
    ..sort((left, right) {
      final time = left.firstObservedAt.compareTo(right.firstObservedAt);
      return time != 0 ? time : left.key.compareTo(right.key);
    });
  final byKey = <String, CaptureConversationTreeSeed<T>>{
    for (final seed in seeds) seed.key: seed,
  };
  final keyByNativeActor = <String, String>{};
  for (final seed in seeds) {
    final identityKey = _nativeActorKey(seed);
    if (identityKey != null) {
      keyByNativeActor.putIfAbsent(identityKey, () => seed.key);
    }
  }

  final parentByKey = <String, String>{};
  for (final seed in seeds) {
    final client = seed.client;
    final sessionId = seed.sessionId;
    if (client == null || sessionId == null) continue;
    for (final parentActorId in seed.parentActorIds) {
      final parentKey =
          keyByNativeActor[_identityKey(client, sessionId, parentActorId)];
      if (parentKey == null || parentKey == seed.key) continue;
      if (_wouldCreateCycle(seed.key, parentKey, parentByKey)) continue;
      parentByKey[seed.key] = parentKey;
      break;
    }
  }

  final childrenByKey = <String, List<String>>{};
  for (final relation in parentByKey.entries) {
    childrenByKey
        .putIfAbsent(relation.value, () => <String>[])
        .add(relation.key);
  }
  int compareKeys(String left, String right) {
    final leftSeed = byKey[left]!;
    final rightSeed = byKey[right]!;
    final time = leftSeed.firstObservedAt.compareTo(rightSeed.firstObservedAt);
    return time != 0 ? time : left.compareTo(right);
  }

  for (final children in childrenByKey.values) {
    children.sort(compareKeys);
  }
  final roots =
      seeds
          .where((seed) => !parentByKey.containsKey(seed.key))
          .map((seed) => seed.key)
          .toList(growable: false)
        ..sort(compareKeys);

  final branchStatus = <String, String>{};
  String resolveStatus(String key) {
    final cached = branchStatus[key];
    if (cached != null) return cached;
    var status = byKey[key]!.status;
    for (final child in childrenByKey[key] ?? const <String>[]) {
      status = _strongerStatus(status, resolveStatus(child));
    }
    branchStatus[key] = status;
    return status;
  }

  final result = <CaptureConversationTreeEntry<T>>[];
  void append(
    String key,
    String? parentKey,
    int depth, {
    required bool isLastSibling,
    required List<bool> ancestorHasNextSibling,
  }) {
    final children = childrenByKey[key] ?? const <String>[];
    result.add(
      CaptureConversationTreeEntry<T>(
        value: byKey[key]!.value,
        key: key,
        parentKey: parentKey,
        depth: depth,
        hasChildren: children.isNotEmpty,
        isLastSibling: isLastSibling,
        ancestorHasNextSibling: List.unmodifiable(ancestorHasNextSibling),
        status: resolveStatus(key),
      ),
    );
    for (final child in children.indexed) {
      append(
        child.$2,
        key,
        depth + 1,
        isLastSibling: child.$1 == children.length - 1,
        ancestorHasNextSibling: depth == 0
            ? ancestorHasNextSibling
            : [...ancestorHasNextSibling, !isLastSibling],
      );
    }
  }

  for (final root in roots.indexed) {
    append(
      root.$2,
      null,
      0,
      isLastSibling: root.$1 == roots.length - 1,
      ancestorHasNextSibling: const [],
    );
  }
  return List.unmodifiable(result);
}

String? _nativeActorKey<T>(CaptureConversationTreeSeed<T> seed) {
  final client = seed.client;
  final sessionId = seed.sessionId;
  final actorId = seed.actorId;
  if (client == null || sessionId == null || actorId == null) return null;
  return _identityKey(client, sessionId, actorId);
}

String _identityKey(String client, String sessionId, String actorId) =>
    '$client\u0000$sessionId\u0000$actorId';

bool _wouldCreateCycle(
  String childKey,
  String candidateParentKey,
  Map<String, String> parentByKey,
) {
  var cursor = candidateParentKey;
  final visited = <String>{childKey};
  while (visited.add(cursor)) {
    final parent = parentByKey[cursor];
    if (parent == null) return false;
    cursor = parent;
  }
  return true;
}

String _strongerStatus(String left, String right) {
  const rank = <String, int>{
    'succeeded': 0,
    'canceled': 1,
    'failed': 2,
    'pending': 3,
  };
  return (rank[right] ?? -1) > (rank[left] ?? -1) ? right : left;
}
