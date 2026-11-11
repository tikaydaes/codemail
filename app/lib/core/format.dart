/// Codes are grouped for reading; the grouping is never copied.
String showCode(String code) {
  if (RegExp(r'^\d{6}$').hasMatch(code)) {
    return '${code.substring(0, 3)} ${code.substring(3)}';
  }
  if (RegExp(r'^\d{8}$').hasMatch(code)) {
    return '${code.substring(0, 4)} ${code.substring(4)}';
  }
  return code;
}

String ago(DateTime? time, {DateTime? now}) {
  if (time == null) return '';
  final s = (now ?? DateTime.now())
      .difference(time)
      .inSeconds
      .clamp(0, 1 << 31);
  if (s < 60) return '$s s ago';
  if (s < 3600) return '${s ~/ 60} min ago';
  if (s < 86400) return '${s ~/ 3600} h ago';
  return '${s ~/ 86400} d ago';
}

/// Accepts "blahaj.example", "https://blahaj.example/" and similar, and
/// returns the server's base URL without a trailing slash, or null.
String? normalizeServer(String input) {
  var s = input.trim();
  if (s.isEmpty || s.contains(RegExp(r'\s'))) return null;
  if (!s.contains('://')) s = 'https://$s';
  final u = Uri.tryParse(s);
  if (u == null ||
      u.host.isEmpty ||
      (u.scheme != 'https' && u.scheme != 'http')) {
    return null;
  }
  return '${u.scheme}://${u.host}${u.hasPort ? ':${u.port}' : ''}';
}
