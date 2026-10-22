/// Syntax only: the Runtime remains the authority for cursor proof, source,
/// Exchange and visibility validation. Preserve the opaque value unchanged.
bool isValidContentCursor(String cursor) =>
    cursor.isNotEmpty &&
    cursor.length <= 2048 &&
    _contentCursorSyntax.hasMatch(cursor);

final _contentCursorSyntax = RegExp(r'^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)?$');
