import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:codemail/core/api.dart';
import 'package:codemail/core/format.dart';
import 'package:codemail/core/sse.dart';
import 'package:codemail/core/watcher.dart';
import 'package:codemail/session_store.dart';
import 'package:codemail/start_at_login.dart';
import 'package:flutter_test/flutter_test.dart';

/// A stand-in Codemail server with just enough behaviour to exercise the
/// client: tokens that expire on demand, pairing, messages and a stream.
class FakeServer {
  late HttpServer _http;
  String get url => 'http://127.0.0.1:${_http.port}';

  int issued = 0;
  int refreshes = 0;
  String validAccess = '';
  String validRefresh = '';
  bool pairingConfirmed = false;
  String? lastEventId;
  final streams = <HttpResponse>[];

  Future<void> start() async {
    _http = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    _http.listen(_handle);
  }

  Future<void> stop() => _http.close(force: true);

  void _json(HttpRequest r, int status, Object body) {
    r.response
      ..statusCode = status
      ..headers.contentType = ContentType.json
      ..write(jsonEncode(body));
    r.response.close();
  }

  Map<String, Object> _newTokens() {
    issued++;
    validAccess = 'access-$issued';
    validRefresh = 'refresh-$issued';
    return {
      'access_token': validAccess,
      'refresh_token': validRefresh,
      'token_type': 'Bearer',
      'expires_in': 3600,
    };
  }

  Future<void> _handle(HttpRequest r) async {
    final path = r.uri.path;
    if (path == '/api/v1/server') {
      return _json(r, 200, {'registration': 'invite', 'needs_setup': false});
    }
    if (path == '/oauth/token') {
      final form = Uri.splitQueryString(await utf8.decoder.bind(r).join());
      switch (form['grant_type']) {
        case 'authorization_code':
          if (form['code'] == 'good-code' &&
              (form['code_verifier'] ?? '').length >= 43) {
            return _json(r, 200, _newTokens());
          }
          return _json(r, 400, {
            'error': 'invalid_grant',
            'error_description': 'unknown or expired code',
          });
        case 'refresh_token':
          refreshes++;
          await Future<void>.delayed(const Duration(milliseconds: 30));
          if (form['refresh_token'] == validRefresh) {
            return _json(r, 200, _newTokens());
          }
          return _json(r, 400, {'error': 'invalid_grant'});
        case pairingGrant:
          if (!pairingConfirmed) {
            return _json(r, 400, {'error': 'authorization_pending'});
          }
          return _json(r, 200, _newTokens());
      }
      return _json(r, 400, {'error': 'unsupported_grant_type'});
    }
    if (r.headers.value('authorization') != 'Bearer $validAccess') {
      return _json(r, 401, {'error': 'sign in required'});
    }
    if (path == '/api/v1/me') {
      return _json(r, 200, {
        'user': {'username': 'kaity'},
      });
    }
    if (path == '/api/v1/messages') {
      return _json(r, 200, {
        'messages': [
          {
            'id': 7,
            'kind': 'code',
            'service': 'Northwind Bank',
            'code': '834201',
            'auth': 'pass',
            'flags': <String>[],
            'received_at': 1794400000,
          },
        ],
      });
    }
    if (path == '/api/v1/events') {
      lastEventId = r.headers.value('last-event-id');
      r.response
        ..headers.contentType = ContentType('text', 'event-stream')
        ..bufferOutput = false
        ..write(': connected\n\n');
      await r.response.flush();
      streams.add(r.response);
      return;
    }
    _json(r, 404, {'error': 'not found'});
  }

  Future<void> push(String type, int id, Map<String, Object> data) async {
    for (final s in streams) {
      s.write('id: $id\nevent: $type\ndata: ${jsonEncode(data)}\n\n');
      await s.flush();
    }
  }
}

void main() {
  late FakeServer server;
  setUp(() async {
    server = FakeServer();
    await server.start();
  });
  tearDown(() => server.stop());

  test('formats codes and server addresses', () {
    expect(showCode('834201'), '834 201');
    expect(showCode('28493575'), '2849 3575');
    expect(showCode('X7K9P2'), 'X7K9P2');
    expect(normalizeServer(' mail.example.com/ '), 'https://mail.example.com');
    expect(
      normalizeServer('http://localhost:8080/#/codes'),
      'http://localhost:8080',
    );
    expect(normalizeServer('not a url'), isNull);
    expect(
      ago(DateTime(2026, 1, 1, 12, 0, 0), now: DateTime(2026, 1, 1, 12, 0, 42)),
      '42 s ago',
    );
  });

  test('start at login writes and removes a Linux autostart entry', () async {
    final home = await Directory.systemTemp.createTemp('codemail-test');
    addTearDown(() => home.delete(recursive: true));
    final start = StartAtLogin(
      executable: '/opt/codemail/codemail',
      environment: {'HOME': home.path},
      windows: false,
    );
    expect(await start.isEnabled(), isFalse);
    await start.setEnabled(true);
    expect(
      start.desktopFile.path,
      '${home.path}/.config/autostart/codemail.desktop',
    );
    expect(
      await start.desktopFile.readAsString(),
      contains('Exec="/opt/codemail/codemail"'),
    );
    expect(await start.isEnabled(), isTrue);
    await start.setEnabled(false);
    expect(await start.isEnabled(), isFalse);
  });

  test('PKCE challenge is the SHA-256 of the verifier', () {
    final p = Pkce.generate();
    expect(p.verifier.length, greaterThanOrEqualTo(43));
    expect(p.challenge, isNot(contains('=')));
    expect(Pkce.generate().verifier, isNot(p.verifier));
  });

  test('parses an event stream, ignoring keep-alives', () async {
    final bytes = Stream.fromIterable([
      utf8.encode(': connected\n\nid: 5\nevent: mess'),
      utf8.encode(
        'age\ndata: {"id":5}\n\n: ping\n\nevent: delete\ndata: {"id":5}\n\n',
      ),
    ]);
    final events = await parseSse(bytes).toList();
    expect(events.map((e) => '${e.type}/${e.id}/${e.data}'), [
      'message/5/{"id":5}',
      'delete/null/{"id":5}',
    ]);
  });

  test('signs in with a code, then calls the API', () async {
    final saved = <String>[];
    final api = CodemailApi(server.url, onTokens: (t) => saved.add(t.access));
    await api.probe();
    final pkce = Pkce.generate();
    final url = api.authorizeUrl(
      pkce: pkce,
      redirectUri: 'http://127.0.0.1:1/callback',
      deviceName: 'Test',
      platform: 'test',
    );
    expect(url.path, '/oauth/authorize');
    expect(url.queryParameters['code_challenge'], pkce.challenge);

    await expectLater(
      api.exchangeCode(code: 'bad', pkce: pkce, redirectUri: 'x'),
      throwsA(isA<ApiException>()),
    );
    await api.exchangeCode(
      code: 'good-code',
      pkce: pkce,
      redirectUri: 'http://127.0.0.1:1/callback',
    );
    expect(saved, ['access-1']);

    final messages = await api.messages(kind: 'code');
    expect(messages.single.code, '834201');
    expect(messages.single.title, 'Northwind Bank');
  });

  test('renews an expired token once for concurrent calls', () async {
    final api = CodemailApi(server.url);
    await api.exchangeCode(
      code: 'good-code',
      pkce: Pkce.generate(),
      redirectUri: 'x',
    );
    server.validAccess =
        'revoked-elsewhere'; // the server no longer accepts our access token
    final results = await Future.wait([
      api.me(),
      api.messages(kind: 'code'),
      api.me(),
    ]);
    expect(results, hasLength(3));
    expect(
      server.refreshes,
      1,
      reason: 'refresh tokens rotate, so concurrent renewals would sign the device out',
    );
    expect(api.tokens!.access, 'access-2');
  });

  test('reports a disconnected device', () async {
    final api = CodemailApi(server.url);
    await api.exchangeCode(
      code: 'good-code',
      pkce: Pkce.generate(),
      redirectUri: 'x',
    );
    server
      ..validAccess = 'gone'
      ..validRefresh = 'gone';
    await expectLater(api.me(), throwsA(isA<SignedOutException>()));
    expect(api.tokens, isNull);
  });

  test('pairing waits for confirmation', () async {
    final api = CodemailApi(server.url);
    await expectLater(
      api.pair(code: 'c', deviceName: 'Pixel', platform: 'android'),
      throwsA(isA<PairingPendingException>()),
    );
    server.pairingConfirmed = true;
    await api.pair(code: 'c', deviceName: 'Pixel', platform: 'android');
    expect(api.tokens!.access, 'access-1');
  });

  test('streams live events and resumes from the last message', () async {
    final api = CodemailApi(server.url);
    await api.exchangeCode(
      code: 'good-code',
      pkce: Pkce.generate(),
      redirectUri: 'x',
    );
    final seen = <String>[];
    final gotMessage = Completer<void>();
    final sub = api.events(lastMessageId: 41).listen((e) {
      seen.add(e.type);
      if (e.type == 'message') gotMessage.complete();
    });
    while (server.streams.isEmpty) {
      await Future<void>.delayed(const Duration(milliseconds: 10));
    }
    await server.push('message', 42, {
      'id': 42,
      'kind': 'code',
      'code': '111222',
    });
    await gotMessage.future.timeout(const Duration(seconds: 5));
    expect(seen, ['open', 'message']);
    expect(server.lastEventId, '41');
    await sub.cancel();
  });

  test('two holders of one saved session renew only once', () async {
    // The app and its background service share saved tokens. When one has
    // renewed, the other must pick those up instead of renewing again.
    final secrets = MemorySecretStore();
    final store = SessionStore(secrets);
    CodemailApi holder() => CodemailApi(
      server.url,
      onTokens: (t) => store.save(server.url, t),
      loadTokens: () async => (await store.load())?.tokens,
    );
    final app = holder();
    await app.exchangeCode(
      code: 'good-code',
      pkce: Pkce.generate(),
      redirectUri: 'x',
    );
    final service = holder()..tokens = (await store.load())!.tokens;

    server.validAccess = 'expired-on-server';
    await app.me(); // the app renews and saves
    expect(server.refreshes, 1);
    await service.me(); // the service still holds the old tokens
    expect(
      server.refreshes,
      1,
      reason: 'the service should adopt the saved tokens',
    );
    expect(service.tokens!.access, app.tokens!.access);
  });

  test('the watcher announces each new code once', () async {
    final api = CodemailApi(server.url);
    await api.exchangeCode(
      code: 'good-code',
      pkce: Pkce.generate(),
      redirectUri: 'x',
    );
    final announced = <String>[];
    int? saved;
    CodeWatcher watcher() => CodeWatcher(
      api: api,
      onCode: (m) async => announced.add(m.code),
      readLastId: () async => saved,
      writeLastId: (id) async => saved = id,
    );
    Future<void> settle() =>
        Future<void>.delayed(const Duration(milliseconds: 150));
    // Waits for the watcher to reach a message rather than for a fixed time,
    // then a little longer so that anything unexpected would show up too.
    Future<void> reached(int id) async {
      for (var i = 0; i < 100 && saved != id; i++) {
        await Future<void>.delayed(const Duration(milliseconds: 50));
      }
      await settle();
    }

    final now = DateTime.now().millisecondsSinceEpoch ~/ 1000;
    Map<String, Object> message(
      int id,
      String code, {
      int age = 0,
      String kind = 'code',
    }) => {
      'id': id,
      'kind': kind,
      'code': code,
      'service': 'S',
      'flags': <String>[],
      'received_at': now - age,
    };

    final first = watcher();
    await first.start();
    expect(
      saved,
      7,
      reason: 'starts from the newest existing message without announcing it',
    );
    while (server.streams.isEmpty) {
      await settle();
    }
    await server.push('message', 8, message(8, '111222'));
    await server.push(
      'message',
      8,
      message(8, '111222'),
    ); // a replay of the same message
    await server.push(
      'message',
      9,
      message(9, '', kind: 'other'),
    ); // no code or link
    await server.push(
      'message',
      10,
      message(10, '999000', age: 3600),
    ); // missed long ago
    await server.push('message', 11, message(11, '333444'));
    await reached(11);
    expect(announced, ['111222', '333444']);
    expect(saved, 11);
    first.stop();

    // After a restart it resumes from where it stopped.
    server.streams.clear();
    final second = watcher();
    await second.start();
    while (server.streams.isEmpty) {
      await settle();
    }
    expect(server.lastEventId, '11');
    await server.push('message', 11, message(11, '333444'));
    await server.push('message', 12, message(12, '555666'));
    await reached(12);
    expect(announced, ['111222', '333444', '555666']);
    second.stop();
  });
}
