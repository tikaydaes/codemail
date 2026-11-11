import 'dart:async';
import 'dart:convert';
import 'dart:math';

import 'api.dart';
import 'models.dart';
import 'sse.dart';

/// Holds the live connection open and reports each new code or link once.
/// It has no user interface: the Android background service runs one so
/// codes are announced even when the app itself is closed.
class CodeWatcher {
  CodeWatcher({
    required this.api,
    required this.onCode,
    required this.readLastId,
    required this.writeLastId,
    this.onSignedOut,
    this.now = DateTime.now,
  });

  final CodemailApi api;

  /// Called for a newly arrived message that has a code or link.
  final Future<void> Function(Message) onCode;

  /// The newest message ID already handled, kept across restarts.
  final Future<int?> Function() readLastId;
  final Future<void> Function(int) writeLastId;

  /// Called when the server no longer accepts this device.
  final void Function()? onSignedOut;
  final DateTime Function() now;

  StreamSubscription<SseEvent>? _events;
  Timer? _retry;
  int _backoff = 1;
  int? _lastId;
  bool _running = false;

  bool get running => _running;

  Future<void> start() async {
    if (_running) return;
    _running = true;
    _lastId = await readLastId();
    if (_lastId == null) {
      // First run: start from the newest message, so mail that arrived
      // before now is not announced as if it were new.
      try {
        final lists = await Future.wait([
          api.messages(kind: 'code', limit: 1),
          api.messages(kind: 'other', limit: 1),
        ]);
        var newest = 0;
        for (final m in lists.expand((l) => l)) {
          newest = max(newest, m.id);
        }
        await _advance(newest);
      } on SignedOutException {
        return _signedOut();
      } on Exception {
        // Offline: the stream below retries, and replays from the start.
      }
    }
    _connect();
  }

  void stop() {
    _running = false;
    _events?.cancel();
    _events = null;
    _retry?.cancel();
  }

  Future<void> _advance(int id) async {
    if (id > (_lastId ?? -1)) {
      _lastId = id;
      await writeLastId(id);
    }
  }

  void _signedOut() {
    stop();
    onSignedOut?.call();
  }

  void _connect() {
    if (!_running) return;
    _events?.cancel();
    _events = api
        .events(lastMessageId: _lastId)
        .listen(
          _onEvent,
          onError: (Object e) =>
              e is SignedOutException ? _signedOut() : _reconnect(),
          onDone: _reconnect,
          cancelOnError: true,
        );
  }

  void _reconnect() {
    if (!_running) return;
    _retry?.cancel();
    _retry = Timer(Duration(seconds: _backoff), _connect);
    _backoff = min(_backoff * 2, 60);
  }

  Future<void> _onEvent(SseEvent e) async {
    if (e.type == 'open') {
      _backoff = 1;
      return;
    }
    if (e.type != 'message') return;
    final Message m;
    try {
      m = Message.fromJson(jsonDecode(e.data) as Map<String, dynamic>);
    } on Object {
      return;
    }
    final isNew = m.id > (_lastId ?? 0);
    await _advance(m.id);
    // After a long gap the server replays what was missed. Only mail that
    // is still fresh is worth an alert; the rest is in the list.
    final recent =
        m.receivedAt != null &&
        now().difference(m.receivedAt!) < const Duration(minutes: 10);
    if (isNew && recent && m.kind == 'code') await onCode(m);
  }
}
