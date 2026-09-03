import 'package:flutter/material.dart';

/// ViberMate's compact traffic-recorder mark.
///
/// The paired traces represent request and response traffic. The amber core is
/// the durable audit record produced while that traffic passes through.
final class ViberMateMark extends StatelessWidget {
  const ViberMateMark({this.size = 23, this.framed = false, super.key});

  final double size;
  final bool framed;

  @override
  Widget build(BuildContext context) {
    final mark = Semantics(
      label: 'ViberMate',
      image: true,
      child: CustomPaint(
        size: Size.square(size),
        painter: _ViberMateMarkPainter(
          bright: framed || Theme.of(context).brightness == Brightness.dark,
        ),
      ),
    );
    if (!framed) return SizedBox.square(dimension: size, child: mark);
    return SizedBox.square(
      dimension: size,
      child: DecoratedBox(
        decoration: BoxDecoration(
          color: const Color(0xFF10263D),
          borderRadius: BorderRadius.circular(size * 0.23),
        ),
        child: Padding(padding: EdgeInsets.all(size * 0.14), child: mark),
      ),
    );
  }
}

final class _ViberMateMarkPainter extends CustomPainter {
  const _ViberMateMarkPainter({required this.bright});

  final bool bright;

  @override
  void paint(Canvas canvas, Size size) {
    final request = Paint()
      ..color = bright ? const Color(0xFF55D9C0) : const Color(0xFF168C7C)
      ..strokeWidth = size.width * 0.07
      ..strokeCap = StrokeCap.round
      ..strokeJoin = StrokeJoin.round
      ..style = PaintingStyle.stroke;
    final responsePaint = Paint()
      ..color = bright ? const Color(0xFF70B9EE) : const Color(0xFF397FB9)
      ..strokeWidth = size.width * 0.07
      ..strokeCap = StrokeCap.round
      ..strokeJoin = StrokeJoin.round
      ..style = PaintingStyle.stroke;
    final coreBackdrop = Paint()..color = const Color(0xFF102943);
    final coreBorder = Paint()
      ..color = const Color(0xFF58748C)
      ..strokeWidth = size.width * 0.018
      ..style = PaintingStyle.stroke;
    final core = Paint()..color = const Color(0xFFFFB627);
    final w = size.width;
    final h = size.height;

    final forward = Path()
      ..moveTo(w * 0.06, h * 0.33)
      ..lineTo(w * 0.31, h * 0.33)
      ..quadraticBezierTo(w * 0.45, h * 0.33, w * 0.45, h * 0.50)
      ..quadraticBezierTo(w * 0.45, h * 0.67, w * 0.59, h * 0.67)
      ..lineTo(w * 0.94, h * 0.67);
    final responsePath = Path()
      ..moveTo(w * 0.94, h * 0.33)
      ..lineTo(w * 0.69, h * 0.33)
      ..quadraticBezierTo(w * 0.55, h * 0.33, w * 0.55, h * 0.50)
      ..quadraticBezierTo(w * 0.55, h * 0.67, w * 0.41, h * 0.67)
      ..lineTo(w * 0.06, h * 0.67);
    canvas
      ..drawPath(forward, request)
      ..drawPath(responsePath, responsePaint);
    canvas
      ..drawCircle(Offset(w * 0.5, h * 0.5), w * 0.14, coreBackdrop)
      ..drawCircle(Offset(w * 0.5, h * 0.5), w * 0.14, coreBorder)
      ..drawCircle(Offset(w * 0.5, h * 0.5), w * 0.052, core);
  }

  @override
  bool shouldRepaint(covariant _ViberMateMarkPainter oldDelegate) =>
      bright != oldDelegate.bright;
}
