import 'package:flutter/material.dart';

import '../../core/design/workbench_widgets.dart';
import '../../core/i18n/app_copy.dart';

final class ControlFailureNotice extends StatelessWidget {
  const ControlFailureNotice({
    required this.message,
    required this.copy,
    this.diagnostic,
    super.key,
  });

  final String message;
  final String? diagnostic;
  final AppCopy copy;

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      InlineNotice(message: copy.maybe(message) ?? message, error: true),
      if (diagnostic case final value?)
        ExpansionTile(
          dense: true,
          visualDensity: VisualDensity.compact,
          title: Text(
            copy('error.diagnostic_details'),
            style: Theme.of(context).textTheme.bodySmall,
          ),
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
              child: Align(
                alignment: Alignment.centerLeft,
                child: SelectableText(value),
              ),
            ),
          ],
        ),
    ],
  );
}
