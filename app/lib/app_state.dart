import 'dart:async';
import 'dart:convert';
import 'dart:io' show Platform;
import 'dart:math';

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';
import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';

import 'core/api.dart';
import 'core/format.dart';
import 'core/models.dart';
import 'core/sse.dart';
import 'session_store.dart';

export 'session_store.dart' show MemorySecretStore, SecretStore;

/// Shows a system notification.
typedef Notify = Future<void> Function(
  String title,
  String body, {
  int? messageId,
});

/// Set when the user taps a notification: the message to open.
final openMessageRequest = ValueNotifier<int?>(null);

bool get isDesktop =>
    !kIsWeb && (Platform.isMacOS || Platform.isWindows || Platform.isLinux);
bool get isMobile => !kIsWeb && (Platform.isAndroid || Platform.isIOS);

/// The app's state: the connection to one server, its messages, the live
/// stream and the user's preferences.
class AppState extends ChangeNotifier {
  AppState({
    required this.secrets,
    required this.prefs,
    this.notify,
    this.clientFactory,
  });

  final SecretStore secrets;
  final SharedPreferences prefs;
  final Notify? notify;
  final http.Client Function()? clientFactory;

  late final SessionStore session = SessionStore(secrets);

  CodemailApi? api;
  String? server;
  String? username;
  List<Message> codes = [];
  List<Message> other = [];
  bool loaded = false; // the saved session has been read
  bool live = false;
  String? error;

  StreamSubscription<SseEvent>? _events;
  Timer? _retry;
  Timer? _clearClipboard;
  int _backoff = 1;
  int? _lastId;
  bool _disposed = false;

  bool get signedIn => api != null;

  /// Whether the Android background service is the one showing notifications.
  bool get backgroundAnnounces =>
      !kIsWeb && Platform.isAndroid && stayConnected;

  // --- preferences ---

  bool get autoCopy => prefs.getBool('autoCopy') ?? isDesktop;
  int get clearClipboardSeconds => prefs.getInt('clearClipboardSeconds') ?? 60;
  bool get showCodeInNotifications =>
      prefs.getBool('showCodeInNotifications') ?? true;
  bool get stayConnected => prefs.getBool('stayConnected') ?? true;
  bool get alwaysConfirmLinks => prefs.getBool('alwaysConfirmLinks') ?? false;

  Future<void> setAutoCopy(bool v) => _set(() => prefs.setBool('autoCopy', v));
  Future<void> setClearClipboardSeconds(int v) =>
      _set(() => prefs.setInt('clearClipboardSeconds', v));
  Future<void> setShowCodeInNotifications(bool v) =>
      _set(() => prefs.setBool('showCodeInNotifications', v));
  Future<void> setAlwaysConfirmLinks(bool v) =>
      _set(() => prefs.setBool('alwaysConfirmLinks', v));
  Future<void> setStayConnected(bool v) =>
      _set(() => prefs.setBool('stayConnected', v));

  Future<void> _set(Future<bool> Function() write) async {
    await write();
    notifyListeners();
  }

  // --- session ---

  CodemailApi newApi(String server, {Tokens? tokens}) => CodemailApi(
    server,
    client: clientFactory?.call(),
    tokens: tokens,
    onTokens: (t) => session.save(server, t),
    loadTokens: () async => (await session.load())?.tokens,
  );

  /// Restores the saved session, if there is one.
  Future<void> load() async {
    try {
      final saved = await session.load();
      if (saved != null) {
        await _start(newApi(saved.server, tokens: saved.tokens));
      }
    } on Exception catch (e) {
      error = '$e';
    }
    loaded = true;
    notifyListeners();
  }

  /// Called once sign-in or pairing has produced tokens.
  Future<void> connected(CodemailApi connectedApi) async {
    await _start(connectedApi);
    notifyListeners();
  }

  Future<void> _start(CodemailApi a) async {
    api = a;
    server = a.server;
    error = null;
    _connectEvents();
    await refresh();
  }

  Future<void> signOut() async {
    final a = api;
    _stop();
    await session.clear();
    await a?.signOut();
    a?.close();
    notifyListeners();
  }

  void _stop() {
    _events?.cancel();
    _events = null;
    _retry?.cancel();
    api = null;
    username = null;
    codes = [];
    other = [];
    live = false;
    _lastId = null;
  }

  Future<void> _guard(Future<void> Function() action) async {
    try {
      await action();
      error = null;
    } on SignedOutException catch (e) {
      _stop();
      await session.clear();
      error = e.message;
    } on ApiException catch (e) {
      error = e.message;
    } on Exception {
      error = 'Could not reach ${server ?? 'the server'}.';
    }
    if (!_disposed) notifyListeners();
  }

  /// Reloads both message lists.
  Future<void> refresh() => _guard(() async {
    final a = api;
    if (a == null) return;
    final results = await Future.wait([
      a.me(),
      a.messages(kind: 'code'),
      a.messages(kind: 'other'),
    ]);
    username =
        ((results[0] as Map<String, dynamic>)['user']
                as Map<String, dynamic>)['username']
            as String?;
    codes = results[1] as List<Message>;
    other = results[2] as List<Message>;
    for (final m in [...codes, ...other]) {
      _lastId = max(_lastId ?? 0, m.id);
    }
  });

  // --- live stream ---

  void _connectEvents() {
    final a = api;
    if (a == null || _disposed) return;
    _events?.cancel();
    _events = a
        .events(lastMessageId: _lastId)
        .listen(
          _onEvent,
          onError: _onStreamError,
          onDone: _reconnect,
          cancelOnError: true,
        );
  }

  void _onStreamError(Object e) {
    if (e is SignedOutException) {
      _guard(() async => throw e);
      return;
    }
    _reconnect();
  }

  void _reconnect() {
    if (api == null || _disposed) return;
    if (live) {
      live = false;
      notifyListeners();
    }
    _retry?.cancel();
    _retry = Timer(Duration(seconds: _backoff), _connectEvents);
    _backoff = min(_backoff * 2, 30);
  }

  void _onEvent(SseEvent e) {
    if (e.type == 'open') {
      _backoff = 1;
      live = true;
      notifyListeners();
      return;
    }
    final Map<String, dynamic> data;
    try {
      data = jsonDecode(e.data) as Map<String, dynamic>;
    } on Object {
      return;
    }
    switch (e.type) {
      case 'message':
        final m = Message.fromJson(data);
        final isNew =
            !codes.any((x) => x.id == m.id) && !other.any((x) => x.id == m.id);
        _lastId = max(_lastId ?? 0, m.id);
        _put(m);
        if (isNew && m.kind == 'code') _announce(m);
      case 'update':
        _put(Message.fromJson(data));
      case 'delete':
        final id = data['id'];
        codes = codes.where((m) => m.id != id).toList();
        other = other.where((m) => m.id != id).toList();
    }
    notifyListeners();
  }

  /// Inserts or replaces a message in whichever list it belongs to.
  void _put(Message m) {
    codes = [
      for (final x in codes)
        if (x.id != m.id) x,
    ];
    other = [
      for (final x in other)
        if (x.id != m.id) x,
    ];
    final list = m.kind == 'code' ? codes : other;
    list.add(m);
    list.sort((a, b) => b.id.compareTo(a.id));
  }

  void _announce(Message m) {
    final String body;
    if (m.hasCode) {
      body = showCodeInNotifications ? showCode(m.code) : 'New sign-in code';
    } else {
      body = 'Confirmation link received';
    }
    // On Android the background service announces new codes, whether or
    // not the app is open; announcing here too would show each one twice.
    if (!backgroundAnnounces) {
      notify?.call(
        m.title,
        autoCopy && m.hasCode ? '$body · copied' : body,
        messageId: m.id,
      );
    }
    if (autoCopy && m.hasCode) copyToClipboard(m.code);
  }

  // --- actions ---

  /// Copies text and, if the user wants, clears it again later, but only
  /// if the clipboard still holds it.
  Future<void> copyToClipboard(String text) async {
    await Clipboard.setData(ClipboardData(text: text));
    _clearClipboard?.cancel();
    final seconds = clearClipboardSeconds;
    if (seconds <= 0) return;
    _clearClipboard = Timer(Duration(seconds: seconds), () async {
      final current = await Clipboard.getData(Clipboard.kTextPlain);
      if (current?.text == text) {
        await Clipboard.setData(const ClipboardData(text: ''));
      }
    });
  }

  Future<void> useCode(Message m) async {
    await copyToClipboard(m.code);
    await setUsed(m, true);
  }

  Future<void> setUsed(Message m, bool used) => _guard(() async {
    final a = api;
    if (a == null) return;
    _put(await a.setUsed(m.id, used));
  });

  /// Saves the user's correction of what a message's code and link are.
  Future<void> correct(
    Message m, {
    required String code,
    required String link,
  }) => _guard(() async {
    final a = api;
    if (a == null) return;
    Message? latest;
    if (code != m.code) {
      latest = await a.correct(m.id, kind: 'code', value: code);
    }
    if (link != m.link) {
      latest = await a.correct(m.id, kind: 'link', value: link);
    }
    if (latest != null) _put(latest);
  });

  Future<void> delete(Message m) => _guard(() async {
    final a = api;
    if (a == null) return;
    await a.deleteMessage(m.id);
    codes = codes.where((x) => x.id != m.id).toList();
    other = other.where((x) => x.id != m.id).toList();
  });

  @override
  void dispose() {
    _disposed = true;
    _events?.cancel();
    _retry?.cancel();
    _clearClipboard?.cancel();
    super.dispose();
  }
}
