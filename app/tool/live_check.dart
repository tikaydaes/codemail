// Checks the app's client code against a real Codemail server.
//
//   dart run tool/live_check.dart http://localhost:8080
//
// It prints an authorization URL (open it in a browser signed in to the
// server and choose Allow), then exchanges the returned code, calls the
// API and waits for one live message. e2e/app_client.mjs automates this.
import 'dart:async';
import 'dart:io';

import 'package:codemail/core/api.dart';
import 'package:codemail/core/models.dart';

Future<void> main(List<String> args) async {
  final api = CodemailApi(args.first);
  await api.probe();
  final listener = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
  final redirect = 'http://127.0.0.1:${listener.port}/callback';
  final pkce = Pkce.generate();
  stdout.writeln(
    'AUTHORIZE ${api.authorizeUrl(pkce: pkce, redirectUri: redirect, deviceName: 'Live check', platform: 'dart')}',
  );

  final request = await listener.first.timeout(const Duration(minutes: 2));
  final q = request.uri.queryParameters;
  request.response.write('ok');
  await request.response.close();
  await listener.close();
  if (q['state'] != pkce.state) throw StateError('state mismatch');

  await api.exchangeCode(code: q['code']!, pkce: pkce, redirectUri: redirect);
  final me = await api.me();
  stdout.writeln('ME ${(me['user'] as Map)['username']}');
  final addresses = await api.addresses();
  stdout.writeln(
    'ADDRESSES ${addresses.sharedDomains.map((d) => d.name).join(',')}',
  );
  final alias = await api.generateAlias('Live check');
  stdout.writeln('ALIAS ${alias.address}');

  final got = Completer<void>();
  final sub = api.events().listen((e) async {
    if (e.type == 'open') stdout.writeln('STREAM open');
    if (e.type == 'message' && !got.isCompleted) got.complete();
  });
  await got.future.timeout(const Duration(seconds: 30));
  await sub.cancel();

  final codes = await api.messages(kind: 'code');
  final m = codes.first;
  stdout.writeln('MESSAGE ${m.service} code=${m.code} auth=${m.auth}');
  final detail = await api.message(m.id);
  stdout.writeln('MARKDOWN ${detail.markdown!.replaceAll('\n', ' ')}');
  final used = await api.setUsed(m.id, true);
  stdout.writeln('USED ${used.used}');
  await api.signOut();
  try {
    await CodemailApi(
      args.first,
      tokens: Tokens(
        access: 'x',
        refresh: 'x',
        expiresAt: DateTime.now().add(const Duration(hours: 1)),
      ),
    ).me();
  } on SignedOutException {
    stdout.writeln('REVOKED tokens are refused');
  }
  api.close();
  stdout.writeln('DONE');
}
