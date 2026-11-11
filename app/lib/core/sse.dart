import 'dart:async';
import 'dart:convert';

class SseEvent {
  const SseEvent({required this.type, required this.data, this.id});

  final String type;
  final String data;
  final String? id;
}

/// Parses a Server-Sent Events byte stream. Comment lines (keep-alives)
/// are dropped.
Stream<SseEvent> parseSse(Stream<List<int>> bytes) async* {
  var type = 'message';
  String? id;
  final data = <String>[];
  // cast: byte streams arrive typed as Uint8List or as List<int>.
  final lines = utf8.decoder
      .bind(bytes.cast<List<int>>())
      .transform(const LineSplitter());
  await for (final line in lines) {
    if (line.isEmpty) {
      if (data.isNotEmpty) {
        yield SseEvent(type: type, data: data.join('\n'), id: id);
      }
      type = 'message';
      id = null;
      data.clear();
    } else if (!line.startsWith(':')) {
      final colon = line.indexOf(':');
      final field = colon < 0 ? line : line.substring(0, colon);
      var value = colon < 0 ? '' : line.substring(colon + 1);
      if (value.startsWith(' ')) value = value.substring(1);
      switch (field) {
        case 'event':
          type = value;
        case 'data':
          data.add(value);
        case 'id':
          id = value;
      }
    }
  }
}
