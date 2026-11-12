import 'dart:async';
import 'dart:convert';
import 'dart:math';

import 'package:crypto/crypto.dart';
import 'package:http/http.dart' as http;

import 'models.dart';
import 'sse.dart';

class ApiException implements Exception {
  ApiException(this.message, {this.status = 0});

  final String message;
  final int status;

  @override
  String toString() => message;
}

/// The device's tokens were revoked or have expired beyond renewal.
class SignedOutException extends ApiException {
  SignedOutException()
    : super(
        'This device has been disconnected. Connect it again.',
        status: 401,
      );
}

/// Thrown while a QR pairing is waiting for approval in the web app.
class PairingPendingException extends ApiException {
  PairingPendingException() : super('Waiting for confirmation in the web app.');
}

const pairingGrant = 'urn:codemail:params:oauth:grant-type:pairing-code';

/// PKCE values for one sign-in attempt.
class Pkce {
  Pkce._(this.verifier, this.challenge, this.state);

  factory Pkce.generate([Random? random]) {
    final r = random ?? Random.secure();
    String token(int n) => base64Url
        .encode(List.generate(n, (_) => r.nextInt(256)))
        .replaceAll('=', '');
    final verifier = token(48);
    final challenge = base64Url
        .encode(sha256.convert(ascii.encode(verifier)).bytes)
        .replaceAll('=', '');
    return Pkce._(verifier, challenge, token(16));
  }

  final String verifier;
  final String challenge;
  final String state;
}

/// Everything the app asks of a Codemail server. Has no Flutter
/// dependency, so it runs in plain Dart tests and tools.
class CodemailApi {
  CodemailApi(
    this.server, {
    http.Client? client,
    this.tokens,
    this.onTokens,
    this.loadTokens,
  }) : _http = client ?? http.Client();

  /// Base URL, e.g. https://mail.example.com
  final String server;
  final http.Client _http;
  Tokens? tokens;

  /// Called whenever tokens are issued or renewed, so they can be saved.
  final void Function(Tokens)? onTokens;

  /// Reads the tokens currently saved. The app and its background service
  /// share one saved session, so before renewing, each checks whether the
  /// other has already done so.
  final Future<Tokens?> Function()? loadTokens;

  static bool _expiring(Tokens t) =>
      t.expiresAt.difference(DateTime.now()) < const Duration(seconds: 60);

  /// Switches to the saved tokens if they differ from the ones in use.
  Future<bool> _adoptSaved() async {
    final saved = await loadTokens?.call();
    if (saved == null || saved.access == tokens?.access) return false;
    tokens = saved;
    return true;
  }

  Future<Tokens>? _refreshing;

  Uri _uri(String path, [Map<String, String>? query]) =>
      Uri.parse('$server$path')
          .replace(queryParameters: (query?.isEmpty ?? true) ? null : query);

  // --- connecting ---

  /// Confirms that the address is a Codemail server.
  Future<void> probe() async {
    final http.Response res;
    try {
      res = await _http
          .get(_uri('/api/v1/server'))
          .timeout(const Duration(seconds: 15));
    } on Exception {
      throw ApiException('Could not reach $server.');
    }
    if (res.statusCode != 200 || !res.body.contains('"registration"')) {
      throw ApiException('$server does not look like a Codemail server.');
    }
  }

  Uri authorizeUrl({
    required Pkce pkce,
    required String redirectUri,
    required String deviceName,
    required String platform,
  }) => _uri('/oauth/authorize', {
    'response_type': 'code',
    'client_id': 'codemail-app',
    'redirect_uri': redirectUri,
    'code_challenge': pkce.challenge,
    'code_challenge_method': 'S256',
    'state': pkce.state,
    'device_name': deviceName,
    'platform': platform,
  });

  Future<Tokens> _token(Map<String, String> form) async {
    final res = await _http
        .post(_uri('/oauth/token'), body: form)
        .timeout(const Duration(seconds: 20));
    final body = _json(res.body);
    if (res.statusCode == 200) {
      final t = Tokens.fromJson(body);
      tokens = t;
      onTokens?.call(t);
      return t;
    }
    if (body['error'] == 'authorization_pending') {
      throw PairingPendingException();
    }
    throw ApiException(
      (body['error_description'] as String?)?.isNotEmpty == true
          ? body['error_description'] as String
          : 'The server refused the sign-in (${body['error'] ?? res.statusCode}).',
      status: res.statusCode,
    );
  }

  Future<Tokens> exchangeCode({
    required String code,
    required Pkce pkce,
    required String redirectUri,
  }) => _token({
    'grant_type': 'authorization_code',
    'code': code,
    'code_verifier': pkce.verifier,
    'redirect_uri': redirectUri,
  });

  /// Claims a scanned pairing code. Throws [PairingPendingException] until
  /// the user confirms this device in the web app; call again to poll.
  Future<Tokens> pair({
    required String code,
    required String deviceName,
    required String platform,
  }) => _token({
    'grant_type': pairingGrant,
    'code': code,
    'device_name': deviceName,
    'platform': platform,
  });

  Future<Tokens> _refresh() {
    return _refreshing ??= () async {
      try {
        // Someone else may have renewed already; use theirs if still good.
        if (await _adoptSaved() && !_expiring(tokens!)) return tokens!;
        final current = tokens;
        if (current == null) throw SignedOutException();
        return await _token({
          'grant_type': 'refresh_token',
          'refresh_token': current.refresh,
        });
      } on PairingPendingException {
        rethrow;
      } on ApiException catch (e) {
        if (e.status == 400) {
          tokens = null;
          throw SignedOutException();
        }
        rethrow;
      } finally {
        _refreshing = null;
      }
    }();
  }

  Future<void> signOut() async {
    final current = tokens;
    tokens = null;
    if (current == null) return;
    try {
      await _http
          .post(_uri('/oauth/revoke'), body: {'token': current.refresh})
          .timeout(const Duration(seconds: 10));
    } on Exception {
      // Offline: the device can still be removed from the web app.
    }
  }

  // --- authenticated requests ---

  Future<String> _accessToken() async {
    if (tokens == null) await _adoptSaved();
    final t = tokens;
    if (t == null) throw SignedOutException();
    if (_expiring(t)) return (await _refresh()).access;
    return t.access;
  }

  Future<Map<String, dynamic>> _call(
    String method,
    String path, {
    Map<String, String>? query,
    Object? body,
  }) async {
    Future<http.Response> send(String access) async {
      final req = http.Request(method, _uri(path, query))
        ..headers['Authorization'] = 'Bearer $access';
      if (body != null) {
        req.headers['Content-Type'] = 'application/json';
        req.body = jsonEncode(body);
      }
      return http.Response.fromStream(
        await _http.send(req).timeout(const Duration(seconds: 20)),
      );
    }

    var res = await send(await _accessToken());
    if (res.statusCode == 401) res = await send((await _refresh()).access);
    final data = _json(res.body);
    if (res.statusCode >= 400) {
      throw ApiException(
        data['error'] as String? ?? 'Request failed (${res.statusCode}).',
        status: res.statusCode,
      );
    }
    return data;
  }

  static Map<String, dynamic> _json(String body) {
    try {
      final v = jsonDecode(body);
      return v is Map<String, dynamic> ? v : {};
    } on FormatException {
      return {};
    }
  }

  Future<Map<String, dynamic>> me() => _call('GET', '/api/v1/me');

  Future<List<Message>> messages({
    required String kind,
    String search = '',
    int limit = 100,
  }) async {
    final data = await _call(
      'GET',
      '/api/v1/messages',
      query: {
        'kind': kind,
        'limit': '$limit',
        if (search.isNotEmpty) 'q': search,
      },
    );
    return [
      for (final m in data['messages'] as List)
        Message.fromJson(m as Map<String, dynamic>),
    ];
  }

  Future<Message> message(int id) async => Message.fromJson(
    (await _call('GET', '/api/v1/messages/$id'))['message']
        as Map<String, dynamic>,
  );

  Future<Message> setUsed(int id, bool used) async => Message.fromJson(
    (await _call(
          'PATCH',
          '/api/v1/messages/$id',
          body: {'used': used},
        ))['message']
        as Map<String, dynamic>,
  );

  Future<List<Candidate>> candidates(int id) async => [
    for (final c
        in (await _call('GET', '/api/v1/messages/$id/candidates'))['candidates']
            as List)
      Candidate.fromJson(c as Map<String, dynamic>),
  ];

  /// Records what the code or link really is; an empty value means the
  /// message has none.
  Future<Message> correct(
    int id, {
    required String kind,
    required String value,
  }) async => Message.fromJson(
    (await _call(
          'POST',
          '/api/v1/messages/$id/correction',
          body: {'kind': kind, 'value': value},
        ))['message']
        as Map<String, dynamic>,
  );

  Future<void> deleteMessage(int id) => _call('DELETE', '/api/v1/messages/$id');

  Future<Addresses> addresses() async =>
      Addresses.fromJson(await _call('GET', '/api/v1/addresses'));

  Future<Alias> generateAlias(String label) async => Alias.fromJson(
    (await _call(
          'POST',
          '/api/v1/aliases/generate',
          body: {'label': label},
        ))['alias']
        as Map<String, dynamic>,
  );

  Future<void> deleteAlias(int id) => _call('DELETE', '/api/v1/aliases/$id');

  /// One connection to the live event stream. It ends when the server or
  /// network closes it; the caller reconnects, passing the last message
  /// ID it saw so nothing is missed.
  Stream<SseEvent> events({int? lastMessageId}) async* {
    Future<http.StreamedResponse> open(String access) {
      final req = http.Request('GET', _uri('/api/v1/events'))
        ..headers['Authorization'] = 'Bearer $access'
        ..headers['Accept'] = 'text/event-stream';
      if (lastMessageId != null) {
        req.headers['Last-Event-ID'] = '$lastMessageId';
      }
      return _http.send(req).timeout(const Duration(seconds: 20));
    }

    var res = await open(await _accessToken());
    if (res.statusCode == 401) {
      await res.stream.drain<void>();
      res = await open((await _refresh()).access);
    }
    if (res.statusCode != 200) {
      await res.stream.drain<void>();
      throw ApiException(
        'Live updates are unavailable (${res.statusCode}).',
        status: res.statusCode,
      );
    }
    yield const SseEvent(type: 'open', data: '');
    // The server sends a keep-alive every 25 seconds, so a longer silence
    // means the connection is dead.
    yield* parseSse(res.stream.timeout(const Duration(seconds: 70)));
  }

  void close() => _http.close();
}
